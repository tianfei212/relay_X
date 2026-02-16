package control

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"relay-x/v4/config"
	v4errors "relay-x/v4/errors"
	"relay-x/v4/ports"
	"relay-x/v4/relay"
	"relay-x/v4/status"
)

type Client struct {
	Conn net.Conn
	Enc  *json.Encoder
	mu   sync.Mutex

	Role    Role
	ID      string
	Token   string
	MaxConn int

	Auth status.AuthStatus

	closed atomic.Bool
}

// Send 向客户端发送一条控制面消息（线程安全）。
// 参数：
// - msg: 消息信封（type + payload）
// 返回：
// - error: 发送失败原因
func (c *Client) Send(msg Envelope) error {
	if c.closed.Load() {
		return net.ErrClosed
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.Enc.Encode(msg)
}

// Close 关闭客户端连接（幂等）。
func (c *Client) Close() {
	if c.closed.Swap(true) {
		return
	}
	_ = c.Conn.Close()
}

type Hub struct {
	cfg config.Config

	relay   *relay.Manager
	zmqPool *ports.Pool
	srtPool *ports.Pool

	mu      sync.RWMutex
	clients map[net.Conn]*Client
	servers map[string]*Client
	fe      map[string]*Client

	gwStatus      status.GatewayStatus
	started       time.Time
	lastBroadcast atomic.Int64

	sampler *sysSampler
}

// NewHub 创建控制平面 Hub。
// 职责：
// - 管理控制连接注册、在线列表与广播
// - 为客户端分配端口并下发配对信息
// 参数：
// - cfg: 全局配置
// - relayMgr: 数据面管理器（用于遥测）
// - zmqPool/srtPool: 端口池
func NewHub(cfg config.Config, relayMgr *relay.Manager, zmqPool, srtPool *ports.Pool) *Hub {
	h := &Hub{
		cfg:      cfg,
		relay:    relayMgr,
		zmqPool:  zmqPool,
		srtPool:  srtPool,
		clients:  make(map[net.Conn]*Client),
		servers:  make(map[string]*Client),
		fe:       make(map[string]*Client),
		gwStatus: status.GatewayStarting,
		started:  time.Now(),
		sampler:  newSysSampler(),
	}
	h.lastBroadcast.Store(0)
	return h
}

// Start 启动控制平面 TCP 服务并开始接受连接。
// 使用说明：
// - 连接首条消息必须是 register
// - 支持 HTTP GET /status 返回健康状态
// 参数：
// - ctx: 上下文，用于关闭监听与后台循环
// 返回：
// - error: 监听失败或退出原因
func (h *Hub) Start(ctx context.Context) error {
	addr := fmt.Sprintf("0.0.0.0:%d", h.cfg.Gateway.TCPPort)
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	h.gwStatus = status.GatewayRunning

	go func() {
		<-ctx.Done()
		_ = ln.Close()
	}()

	go h.statusBroadcastLoop(ctx)
	go h.telemetryLoop(ctx)

	for {
		conn, err := ln.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return nil
			}
			continue
		}
		go h.handleConn(ctx, conn)
	}
}

// handleConn 处理单条控制连接：
// - 若检测到 HTTP GET，则转入 /status 处理
// - 否则走 JSON 协议：register -> (pair_request 等)
func (h *Hub) handleConn(ctx context.Context, conn net.Conn) {
	br := bufio.NewReaderSize(conn, 8192)
	_ = conn.SetReadDeadline(time.Now().Add(200 * time.Millisecond))
	prefix, _ := br.Peek(4)
	_ = conn.SetReadDeadline(time.Time{})

	if bytes.HasPrefix(prefix, []byte("GET ")) {
		h.handleHTTP(br, conn)
		return
	}

	dec := json.NewDecoder(br)
	enc := json.NewEncoder(conn)

	_ = conn.SetReadDeadline(time.Now().Add(h.cfg.Gateway.AuthTimeout))
	var first Envelope
	if err := dec.Decode(&first); err != nil {
		_ = conn.Close()
		return
	}
	if first.Type != MsgRegister {
		_ = enc.Encode(Envelope{Type: MsgRegisterAck, Payload: RegisterAckPayload{Status: "error", Code: v4errors.CodeBadRequest, Error: "first message must be register"}})
		_ = conn.Close()
		return
	}
	var reg RegisterPayload
	raw, _ := json.Marshal(first.Payload)
	_ = json.Unmarshal(raw, &reg)

	client := &Client{
		Conn:    conn,
		Enc:     enc,
		Role:    reg.Role,
		ID:      strings.TrimSpace(reg.ID),
		Token:   strings.TrimSpace(reg.Token),
		MaxConn: reg.MaxConn,
		Auth:    status.AuthPending,
	}

	if err := h.validateAndRegister(client); err != nil {
		_ = client.Send(Envelope{Type: MsgRegisterAck, Payload: RegisterAckPayload{Status: "error", Code: v4errors.Code(err), Error: err.Error()}})
		_ = conn.Close()
		return
	}
	_ = conn.SetReadDeadline(time.Time{})
	_ = client.Send(Envelope{Type: MsgRegisterAck, Payload: RegisterAckPayload{Status: "ok"}})

	defer func() {
		h.unregister(client)
		client.Close()
	}()

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		var env Envelope
		if err := dec.Decode(&env); err != nil {
			if errors.Is(err, io.EOF) || errors.Is(err, net.ErrClosed) {
				return
			}
			return
		}
		switch env.Type {
		case MsgPairRequest:
			var pr PairRequestPayload
			b, _ := json.Marshal(env.Payload)
			if err := json.Unmarshal(b, &pr); err != nil {
				_ = client.Send(Envelope{Type: MsgError, Payload: ErrorPayload{Code: v4errors.CodeBadRequest, Error: "invalid pair_request"}})
				continue
			}
			h.handlePairRequest(ctx, client, pr)
		default:
		}
	}
}

// validateAndRegister 校验注册信息并将客户端加入 Hub（会检查 role/id/token 与连接数上限）。
// 参数：
// - c: 客户端对象
// 返回：
// - error: 校验失败原因
func (h *Hub) validateAndRegister(c *Client) error {
	if c.ID == "" || c.Token == "" {
		return v4errors.New(v4errors.CodeBadRequest, "missing id/token")
	}
	if c.MaxConn <= 0 {
		c.MaxConn = 1
	}
	if c.Role != RoleServer && c.Role != RoleClient {
		return v4errors.New(v4errors.CodeBadRequest, "invalid role")
	}

	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.clients) >= h.cfg.Gateway.MaxConnections {
		return v4errors.New(v4errors.CodeUnavailable, "max connections reached")
	}

	if c.Role == RoleServer {
		if _, ok := h.servers[c.ID]; ok {
			return v4errors.New(v4errors.CodeConflict, "duplicate server id")
		}
		h.servers[c.ID] = c
	} else {
		if _, ok := h.fe[c.ID]; ok {
			return v4errors.New(v4errors.CodeConflict, "duplicate client id")
		}
		h.fe[c.ID] = c
	}
	h.clients[c.Conn] = c
	c.Auth = status.AuthApproved
	return nil
}

// unregister 将客户端从 Hub 的连接集合中移除（幂等）。
func (h *Hub) unregister(c *Client) {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.clients, c.Conn)
	if c.Role == RoleServer {
		if cur, ok := h.servers[c.ID]; ok && cur == c {
			delete(h.servers, c.ID)
		}
	} else {
		if cur, ok := h.fe[c.ID]; ok && cur == c {
			delete(h.fe, c.ID)
		}
	}
}

// Broadcast 向所有在线控制连接广播一条消息（发送失败则关闭连接）。
func (h *Hub) Broadcast(env Envelope) {
	h.mu.RLock()
	clients := make([]*Client, 0, len(h.clients))
	for _, c := range h.clients {
		if c != nil && !c.closed.Load() {
			clients = append(clients, c)
		}
	}
	h.mu.RUnlock()

	for _, c := range clients {
		if err := c.Send(env); err != nil {
			c.Close()
		}
	}
}

// handlePairRequest 处理客户端发起的配对请求（仅允许 RoleClient）。
func (h *Hub) handlePairRequest(ctx context.Context, c *Client, pr PairRequestPayload) {
	if c.Role != RoleClient {
		_ = c.Send(Envelope{Type: MsgError, Payload: ErrorPayload{Code: v4errors.CodeBadRequest, Error: "only client can pair_request"}})
		return
	}
	if pr.RequestID == "" {
		pr.RequestID = newID()
	}
	go h.pairLoop(ctx, c, pr)
}

// pairLoop 轮询等待可用端口并完成一次配对下发（最多 50 次，每次间隔 5 秒）。
func (h *Hub) pairLoop(ctx context.Context, c *Client, pr PairRequestPayload) {
	for i := 0; i < 50; i++ {
		select {
		case <-ctx.Done():
			return
		default:
		}
		grant, err := h.tryPairOnce(c, pr)
		if err == nil {
			_ = c.Send(Envelope{Type: MsgPairGrant, Payload: grant})
			h.mu.RLock()
			srv := h.servers[grant.ServerID]
			h.mu.RUnlock()
			if srv != nil {
				_ = srv.Send(Envelope{Type: MsgPairGrant, Payload: grant})
			}
			return
		}
		if v4errors.Code(err) != v4errors.CodeUnavailable {
			_ = c.Send(Envelope{Type: MsgPairError, Payload: ErrorPayload{Code: v4errors.Code(err), Error: err.Error()}})
			return
		}
		time.Sleep(5 * time.Second)
	}
	_ = c.Send(Envelope{Type: MsgPairError, Payload: ErrorPayload{Code: v4errors.CodeNoPortTimeout, Error: "no port available"}})
}

// tryPairOnce 尝试执行一次配对：
// - 选择一台有剩余容量的 server
// - 在端口池中 reserve 所需端口并返回 grant
func (h *Hub) tryPairOnce(c *Client, pr PairRequestPayload) (PairGrantPayload, error) {
	h.mu.RLock()
	var servers []*Client
	if pr.ServerID != "" {
		if s := h.servers[pr.ServerID]; s != nil {
			servers = append(servers, s)
		}
	} else {
		for _, s := range h.servers {
			servers = append(servers, s)
		}
	}
	h.mu.RUnlock()

	if len(servers) == 0 {
		return PairGrantPayload{}, v4errors.New(v4errors.CodeUnavailable, "no server online")
	}
	type cand struct {
		c         *Client
		remaining int
	}
	var cands []cand
	for _, s := range servers {
		used := len(occupiedPortsForServer(h.zmqPool, h.srtPool, s.ID))
		rem := s.MaxConn - used
		if rem <= 0 {
			continue
		}
		cands = append(cands, cand{c: s, remaining: rem})
	}
	if len(cands) == 0 {
		return PairGrantPayload{}, v4errors.New(v4errors.CodeUnavailable, "no server capacity")
	}
	sort.Slice(cands, func(i, j int) bool {
		if cands[i].remaining != cands[j].remaining {
			return cands[i].remaining > cands[j].remaining
		}
		return cands[i].c.ID < cands[j].c.ID
	})

	server := cands[0].c
	sessionID := newID()

	var zmqPort, srtPort int
	if pr.NeedZMQ {
		a, err := h.zmqPool.Reserve(sessionID, server.ID, c.ID)
		if err != nil {
			return PairGrantPayload{}, err
		}
		zmqPort = a.Port
	}
	if pr.NeedSRT {
		a, err := h.srtPool.Reserve(sessionID, server.ID, c.ID)
		if err != nil {
			if zmqPort != 0 {
				_ = h.zmqPool.Release(zmqPort, sessionID)
			}
			return PairGrantPayload{}, err
		}
		srtPort = a.Port
	}

	if zmqPort == 0 && srtPort == 0 {
		return PairGrantPayload{}, v4errors.New(v4errors.CodeBadRequest, "need_zmq/need_srt both false")
	}

	return PairGrantPayload{
		RequestID:   pr.RequestID,
		SessionID:   sessionID,
		ServerID:    server.ID,
		ClientID:    c.ID,
		ServerToken: server.Token,
		ClientToken: c.Token,
		ZMQPort:     zmqPort,
		SRTPort:     srtPort,
	}, nil
}

// statusBroadcastLoop 定期广播后端状态与端口池状态（2s 一次）。
func (h *Hub) statusBroadcastLoop(ctx context.Context) {
	t := time.NewTicker(2 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			h.broadcastStatus()
		}
	}
}

// broadcastStatus 构造并广播 MsgStatus + MsgPortState。
func (h *Hub) broadcastStatus() {
	h.mu.RLock()
	servers := make([]*Client, 0, len(h.servers))
	for _, s := range h.servers {
		servers = append(servers, s)
	}
	h.mu.RUnlock()

	payload := make([]ServerStatusPayload, 0, len(servers))
	cpu := h.sampler.CPUPercent()
	mem := h.sampler.MemMB()

	for _, s := range servers {
		ports := occupiedPortsForServer(h.zmqPool, h.srtPool, s.ID)
		remaining := s.MaxConn - len(ports)
		if remaining < 0 {
			remaining = 0
		}
		payload = append(payload, ServerStatusPayload{
			ServerID:      s.ID,
			OccupiedPorts: ports,
			Remaining:     remaining,
			CPUPercent:    cpu,
			MemMB:         mem,
		})
	}

	h.lastBroadcast.Store(time.Now().UnixMilli())
	h.Broadcast(Envelope{Type: MsgStatus, Payload: payload})
	h.Broadcast(Envelope{Type: MsgPortState, Payload: h.portStatePayload(false)})
}

// telemetryLoop 定期广播数据面遥测指标（1s 一次）。
func (h *Hub) telemetryLoop(ctx context.Context) {
	t := time.NewTicker(1 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			ms := h.relay.SnapshotMetrics()
			out := make([]TelemetryPayload, 0, len(ms))
			for _, m := range ms {
				out = append(out, TelemetryPayload{
					Kind:      string(m.Kind),
					Port:      m.Port,
					SessionID: m.SessionID,
					ServerID:  m.ServerID,
					ClientID:  m.ClientID,
					MbpsUp:    m.MbpsUp,
					MbpsDown:  m.MbpsDown,
					RTTMs:     m.RTTMs,
					LossPct:   m.LossPct,
					RetrPct:   m.RetrPct,
					Quality:   m.Quality,
				})
			}
			h.Broadcast(Envelope{Type: MsgTelemetry, Payload: out})
		}
	}
}

// portStatePayload 汇总端口池快照为协议字段。
// 参数：
// - includeIdlePorts: 是否包含 idle_ports 列表（默认 false，避免 payload 过大）
func (h *Hub) portStatePayload(includeIdlePorts bool) PortStatePayload {
	zmq := h.zmqPool.Snapshot()
	srtS := h.srtPool.Snapshot()
	p := PortStatePayload{
		ZMQ: PortStateKindPayload{
			Total:    zmq.Total,
			Idle:     zmq.Idle,
			Reserved: zmq.Reserved,
			Occupied: zmq.Occupied,
			Blocked:  zmq.Blocked,
		},
		SRT: PortStateKindPayload{
			Total:    srtS.Total,
			Idle:     srtS.Idle,
			Reserved: srtS.Reserved,
			Occupied: srtS.Occupied,
			Blocked:  srtS.Blocked,
		},
	}
	if includeIdlePorts {
		p.ZMQ.IdlePorts = zmq.IdlePorts
		p.SRT.IdlePorts = srtS.IdlePorts
	}
	return p
}

// handleHTTP 处理 HTTP GET 请求（当前仅支持 /status）。
// 参数：
// - br: 已包裹的 Reader（复用 peek 的缓冲）
// - conn: 原始连接
func (h *Hub) handleHTTP(br *bufio.Reader, conn net.Conn) {
	line, err := br.ReadString('\n')
	if err != nil {
		_ = conn.Close()
		return
	}
	parts := strings.Split(strings.TrimSpace(line), " ")
	path := ""
	if len(parts) >= 2 {
		path = parts[1]
	}
	if path != "/status" {
		_, _ = conn.Write([]byte("HTTP/1.1 404 Not Found\r\nContent-Length: 0\r\n\r\n"))
		_ = conn.Close()
		return
	}
	hs := HealthStatusPayload{
		Status:          string(h.gwStatus),
		StartedAtUnixMs: h.started.UnixMilli(),
		NowUnixMs:       time.Now().UnixMilli(),
		Connections:     h.connectionCount(),
		BroadcastUnixMs: h.lastBroadcast.Load(),
		PortState:       h.portStatePayload(false),
	}
	body, _ := json.Marshal(hs)
	resp := fmt.Sprintf("HTTP/1.1 200 OK\r\nContent-Type: application/json\r\nContent-Length: %d\r\n\r\n", len(body))
	_, _ = conn.Write([]byte(resp))
	_, _ = conn.Write(body)
	_ = conn.Close()
}

// connectionCount 返回当前在线控制连接数量。
func (h *Hub) connectionCount() int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.clients)
}

// occupiedPortsForServer 汇总某个 server 在两个端口池内占用的端口列表。
func occupiedPortsForServer(zmqPool, srtPool *ports.Pool, serverID string) []int {
	var out []int
	out = append(out, poolOccupiedPorts(zmqPool, serverID)...)
	out = append(out, poolOccupiedPorts(srtPool, serverID)...)
	sort.Ints(out)
	return out
}

// poolOccupiedPorts 返回某个 server 在指定端口池内占用的端口列表。
func poolOccupiedPorts(p *ports.Pool, serverID string) []int {
	snap := p.Snapshot()
	var out []int
	for _, port := range snap.OccupiedPorts {
		a, ok := p.Allocation(port)
		if ok && a.ServerID == serverID {
			out = append(out, port)
		}
	}
	return out
}

// newID 生成用于 session/request 的随机 ID。
func newID() string {
	const letters = "0123456789abcdef"
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		now := time.Now().UnixNano()
		return fmt.Sprintf("s%x", now)
	}
	for i := range b {
		b[i] = letters[int(b[i])%len(letters)]
	}
	return string(b)
}
