package relay

import (
	"context"
	"fmt"
	"net"
	"sync"
	"time"

	srt "github.com/datarhei/gosrt"

	v4errors "relay-x/v4/errors"
	v4log "relay-x/v4/log"
	"relay-x/v4/ports"
)

type srtServer struct {
	pool *ports.Pool
	cfg  srt.Config

	mu        sync.RWMutex
	listeners map[int]srt.Listener
	pendingS  map[int]*srtPending
	pendingC  map[int]*srtPending
	sessions  map[int]*srtSession

	onSessionStop func(port int, sessionID string)

	maintOnce sync.Once
}

type srtSession struct {
	*Session
	rawServer srt.Conn
	rawClient srt.Conn
	bcServer  *BufferedConn
	bcClient  *BufferedConn
}

type srtPending struct {
	raw srt.Conn
	bc  *BufferedConn
}

// newSRTServer 创建 SRT 数据面服务（基于 goSRT）。
// 参数：
// - pool: 端口池（用于校验 sessionID 并推进端口状态）
// - cfg: goSRT 配置（延迟、带宽等）
func newSRTServer(pool *ports.Pool, cfg srt.Config) *srtServer {
	return &srtServer{
		pool:      pool,
		cfg:       cfg,
		listeners: make(map[int]srt.Listener),
		pendingS:  make(map[int]*srtPending),
		pendingC:  make(map[int]*srtPending),
		sessions:  make(map[int]*srtSession),
	}
}

// StartPort 在指定端口启动 SRT 监听（幂等）。
// 参数：
// - ctx: 上下文（用于会话生命周期联动）
// - port: 监听端口
// 返回：
// - error: 监听失败原因
func (s *srtServer) StartPort(ctx context.Context, port int) error {
	s.maintOnce.Do(func() { go s.pendingMaintenanceLoop(ctx) })

	s.mu.Lock()
	if _, ok := s.listeners[port]; ok {
		s.mu.Unlock()
		return nil
	}
	s.mu.Unlock()

	addr := fmt.Sprintf("0.0.0.0:%d", port)
	ln, err := srt.Listen("srt", addr, s.cfg)
	if err != nil {
		return v4errors.Wrap(v4errors.CodeConflict, "srt listen failed", err)
	}

	s.mu.Lock()
	if _, ok := s.listeners[port]; ok {
		s.mu.Unlock()
		ln.Close()
		return nil
	}
	s.listeners[port] = ln
	s.mu.Unlock()

	v4log.With(map[string]any{"port": port, "status": "listen_ok"}).Info("SRT 端口开始监听")
	go s.acceptLoop(ctx, port, ln)
	return nil
}

// StopAll 关闭所有监听、挂起连接与已建立会话（幂等）。
func (s *srtServer) StopAll() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for port, ln := range s.listeners {
		ln.Close()
		delete(s.listeners, port)
	}
	for port, p := range s.pendingS {
		if p != nil && p.bc != nil {
			_ = p.bc.Close()
		}
		delete(s.pendingS, port)
	}
	for port, p := range s.pendingC {
		if p != nil && p.bc != nil {
			_ = p.bc.Close()
		}
		delete(s.pendingC, port)
	}
	for port, sess := range s.sessions {
		sess.Stop()
		delete(s.sessions, port)
	}
}

// acceptLoop 接受新连接并交给 handleConn 处理。
func (s *srtServer) acceptLoop(ctx context.Context, port int, ln srt.Listener) {
	for {
		req, err := ln.Accept2()
		if err != nil {
			return
		}
		conn, err := req.Accept()
		if err != nil {
			continue
		}
		go s.handleConn(ctx, port, conn)
	}
}

// handleConn 处理 SRT 数据面连接：
// - 读取 V4HELLO 握手
// - 校验端口池 allocation/sessionID
// - 依据 role 放入 pendingS/pendingC 并尝试配对
func (s *srtServer) handleConn(ctx context.Context, port int, c net.Conn) {
	raw, ok := c.(srt.Conn)
	if !ok {
		_ = c.Close()
		return
	}
	h, r, err := ReadHello(c, 2*time.Second)
	if err != nil {
		v4log.With(map[string]any{"port": port, "status": "hello_error"}).WithError(err).Warn("SRT 连接被拒绝")
		_ = c.Close()
		return
	}
	bc := &BufferedConn{Conn: c, r: r}
	p := &srtPending{raw: raw, bc: bc}

	alloc, ok := s.pool.Allocation(port)
	if !ok || alloc.SessionID != h.SessionID {
		v4log.With(map[string]any{"port": port, "status": "alloc_mismatch", "id": h.ID}).Warn("SRT 连接被拒绝")
		_ = bc.Close()
		return
	}

	role := h.Role
	s.mu.Lock()
	if _, ok := s.sessions[port]; ok {
		s.mu.Unlock()
		v4log.With(map[string]any{"port": port, "status": "port_occupied", "id": h.ID}).Warn("SRT 连接冲突")
		_ = bc.Close()
		return
	}
	if role == "server" {
		if s.pendingS[port] != nil {
			s.mu.Unlock()
			v4log.With(map[string]any{"port": port, "status": "server_dup", "id": h.ID}).Warn("SRT 连接冲突")
			_ = bc.Close()
			return
		}
		s.pendingS[port] = p
		v4log.With(map[string]any{"port": port, "status": "server_pending", "serverID": h.ID}).Info("SRT 连接已挂起等待配对")
		s.mu.Unlock()
		go s.tryPair(ctx, port)
		return
	}
	if role == "client" {
		if s.pendingC[port] != nil {
			s.mu.Unlock()
			v4log.With(map[string]any{"port": port, "status": "client_dup", "id": h.ID}).Warn("SRT 连接冲突")
			_ = bc.Close()
			return
		}
		s.pendingC[port] = p
		v4log.With(map[string]any{"port": port, "status": "client_pending", "clientID": h.ID}).Info("SRT 连接已挂起等待配对")
		s.mu.Unlock()
		go s.tryPair(ctx, port)
		return
	}
	s.mu.Unlock()
	_ = bc.Close()
}

func (s *srtServer) pendingMaintenanceLoop(ctx context.Context) {
	t := time.NewTicker(2 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.maintainPending()
		}
	}
}

func (s *srtServer) maintainPending() {
	s.mu.Lock()
	defer s.mu.Unlock()

	for port, p := range s.pendingS {
		alloc, ok := s.pool.Allocation(port)
		if !ok {
			delete(s.pendingS, port)
			if p != nil && p.bc != nil {
				_ = p.bc.Close()
			}
			continue
		}
		s.pool.TouchReservation(port, alloc.SessionID)
	}
	for port, p := range s.pendingC {
		alloc, ok := s.pool.Allocation(port)
		if !ok {
			delete(s.pendingC, port)
			if p != nil && p.bc != nil {
				_ = p.bc.Close()
			}
			continue
		}
		s.pool.TouchReservation(port, alloc.SessionID)
	}
}

// tryPair 尝试在指定端口把 pending 的 server 与 client 配对成 Session，并推进端口状态为 Occupied。
func (s *srtServer) tryPair(ctx context.Context, port int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.sessions[port] != nil {
		return
	}
	sp := s.pendingS[port]
	cp := s.pendingC[port]
	sc := (*BufferedConn)(nil)
	cc := (*BufferedConn)(nil)
	var rawS, rawC srt.Conn
	if sp != nil {
		sc = sp.bc
		rawS = sp.raw
	}
	if cp != nil {
		cc = cp.bc
		rawC = cp.raw
	}
	if sc == nil || cc == nil {
		return
	}
	delete(s.pendingS, port)
	delete(s.pendingC, port)

	alloc, ok := s.pool.Allocation(port)
	if !ok {
		_ = sc.Close()
		_ = cc.Close()
		return
	}
	sess := NewSession("srt", port, alloc.SessionID, alloc.ServerID, alloc.ClientID, cc, sc)
	ss := &srtSession{
		Session:   sess,
		rawServer: rawS,
		rawClient: rawC,
		bcServer:  sc,
		bcClient:  cc,
	}
	s.sessions[port] = ss
	_ = s.pool.MarkOccupied(port, alloc.SessionID)

	v4log.With(map[string]any{"port": port, "status": "paired", "sessionID": alloc.SessionID}).Info("SRT 会话开始")
	ss.Start(ctx, func() {
		s.mu.Lock()
		delete(s.sessions, port)
		s.mu.Unlock()
		_ = s.pool.Release(port, alloc.SessionID)
		if s.onSessionStop != nil {
			s.onSessionStop(port, alloc.SessionID)
		}
		v4log.With(map[string]any{"port": port, "status": "session_stop", "sessionID": alloc.SessionID}).Info("SRT 会话结束")
	})
}

// SnapshotStats 返回当前会话的 SRT 统计信息快照（RTT、码率、丢包等）。
func (s *srtServer) SnapshotStats() []Metrics {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Metrics, 0, len(s.sessions))
	for port, sess := range s.sessions {
		m := Metrics{
			Kind:      ports.KindSRT,
			Port:      port,
			SessionID: sess.sessionID,
			ServerID:  sess.serverID,
			ClientID:  sess.clientID,
			UpdatedAt: time.Now(),
		}
		var st srt.Statistics
		sess.rawServer.Stats(&st)
		m.RTTMs = st.Instantaneous.MsRTT
		m.MbpsUp = st.Instantaneous.MbpsSentRate
		m.MbpsDown = st.Instantaneous.MbpsRecvRate
		m.LossPct = st.Instantaneous.PktRecvLossRate
		m.RetrPct = st.Instantaneous.PktSendLossRate
		m.Quality = qualityScore(m.MbpsUp, m.MbpsDown, m.RTTMs, m.LossPct)
		out = append(out, m)
	}
	return out
}

// Sessions 返回当前已建立会话列表的快照。
func (s *srtServer) Sessions() []*Session {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]*Session, 0, len(s.sessions))
	for _, sess := range s.sessions {
		out = append(out, sess.Session)
	}
	return out
}

// qualityScore 根据带宽、RTT、丢包计算一个 0~100 的粗略质量分。
func qualityScore(up, down, rttMs, lossPct float64) float64 {
	score := 100.0
	score -= minf(50, lossPct*2.0)
	score -= minf(30, rttMs/10.0)
	if up+down <= 0 {
		score -= 10
	}
	if score < 0 {
		score = 0
	}
	return score
}

// minf 返回两个 float64 中较小的值。
func minf(a, b float64) float64 {
	if a < b {
		return a
	}
	return b
}
