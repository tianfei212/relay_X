package main

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"math"
	"math/rand"
	"net"
	"os"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type Role string

const (
	RoleFE Role = "FE"
)

type MessageType string

const (
	MsgRegister    MessageType = "register"
	MsgAuth        MessageType = "auth"
	MsgPortAlloc   MessageType = "port_alloc"
	MsgPortAcquire MessageType = "port_acquire"
	MsgPortGrant   MessageType = "port_grant"
	MsgPortRelease MessageType = "port_release"
	MsgPortStateQ  MessageType = "port_state_q"
	MsgPortState   MessageType = "port_state"
	MsgKeyExchange MessageType = "key_exchange"
	MsgPing        MessageType = "ping"
	MsgPong        MessageType = "pong"
	MsgTelemetry   MessageType = "telemetry"
	MsgLog         MessageType = "log"
)

type BaseMessage struct {
	Type    MessageType `json:"type"`
	Payload any         `json:"payload,omitempty"`
}

type RegisterPayload struct {
	Role   Role   `json:"role"`
	Secret string `json:"secret,omitempty"`
}

type PortAllocPayload struct {
	ZMQStartPort int `json:"zmq_start_port"`
	ZMQEndPort   int `json:"zmq_end_port"`
	SRTStartPort int `json:"srt_start_port"`
	SRTEndPort   int `json:"srt_end_port"`
}

type PortAcquirePayload struct {
	RequestID  string `json:"request_id,omitempty"`
	NeedZMQ    bool   `json:"need_zmq"`
	NeedSRT    bool   `json:"need_srt"`
	TTLSeconds int    `json:"ttl_seconds,omitempty"`
}

type PortGrantPayload struct {
	RequestID string `json:"request_id,omitempty"`
	SessionID string `json:"session_id"`
	ZMQPort   int    `json:"zmq_port,omitempty"`
	SRTPort   int    `json:"srt_port,omitempty"`
	ExpiresAt int64  `json:"expires_at"`
	Error     string `json:"error,omitempty"`
}

type PortReleasePayload struct {
	SessionID string `json:"session_id"`
}

type PortStatePayload struct {
	ZMQTotal int `json:"zmq_total"`
	SRTTotal int `json:"srt_total"`

	ZMQBusy int `json:"zmq_busy"`
	SRTBusy int `json:"srt_busy"`

	ZMQReadyPorts []int `json:"zmq_ready_ports"`
	SRTReadyPorts []int `json:"srt_ready_ports"`

	ZMQEmptyPorts []int `json:"zmq_empty_ports"`
	SRTEmptyPorts []int `json:"srt_empty_ports"`
}

type PortStateQueryPayload struct {
	NeedZMQ bool `json:"need_zmq,omitempty"`
	NeedSRT bool `json:"need_srt,omitempty"`
}

type KeyExchangePayload struct {
	PublicKey  string `json:"public_key"`
	PrivateKey string `json:"private_key,omitempty"`
}

type ClockPayload struct {
	ClientSendTime int64 `json:"c_send"`
	ServerRecvTime int64 `json:"s_recv"`
	ServerSendTime int64 `json:"s_send"`
}

type TelemetryPayload struct {
	CurrentSpeed float64 `json:"current_speed"`
}

type rawEnvelope struct {
	Type    MessageType     `json:"type"`
	Payload json.RawMessage `json:"payload"`
}

type ControlClient struct {
	id     int
	round  int
	addr   string
	secret string

	conn    net.Conn
	encoder *json.Encoder
	decoder *json.Decoder

	writeMu sync.Mutex

	portAllocCh   chan PortAllocPayload
	portGrantCh   chan PortGrantPayload
	portStateCh   chan PortStatePayload
	keyExchangeCh chan KeyExchangePayload
	pongCh        chan ClockPayload

	lastPingSendNs int64
	lastPingRTTMs  atomic.Int64
}

func newControlClient(id, round int, addr, secret string) *ControlClient {
	return &ControlClient{
		id:            id,
		round:         round,
		addr:          addr,
		secret:        secret,
		portAllocCh:   make(chan PortAllocPayload, 1),
		portGrantCh:   make(chan PortGrantPayload, 8),
		portStateCh:   make(chan PortStatePayload, 8),
		keyExchangeCh: make(chan KeyExchangePayload, 1),
		pongCh:        make(chan ClockPayload, 8),
	}
}

func (c *ControlClient) connect(ctx context.Context) error {
	d := net.Dialer{}
	conn, err := d.DialContext(ctx, "tcp", c.addr)
	if err != nil {
		return fmt.Errorf("连接控制面失败: %w", err)
	}
	c.conn = conn
	c.encoder = json.NewEncoder(conn)
	c.decoder = json.NewDecoder(bufio.NewReader(conn))
	log.Printf("[R%03d][C%02d][控制] 已连接 addr=%s", c.round, c.id, c.addr)
	return nil
}

func (c *ControlClient) close() {
	if c.conn != nil {
		_ = c.conn.Close()
	}
}

func (c *ControlClient) send(msg BaseMessage) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	return c.encoder.Encode(msg)
}

func (c *ControlClient) registerAndAuth(ctx context.Context) (time.Duration, error) {
	start := time.Now()
	if err := c.send(BaseMessage{
		Type: MsgRegister,
		Payload: RegisterPayload{
			Role:   RoleFE,
			Secret: c.secret,
		},
	}); err != nil {
		return 0, fmt.Errorf("发送注册失败: %w", err)
	}

	type authPayload struct {
		Status string `json:"status"`
	}

	if err := c.conn.SetReadDeadline(time.Now().Add(10 * time.Second)); err != nil {
		return 0, fmt.Errorf("设置读超时失败: %w", err)
	}

	for {
		var env rawEnvelope
		if err := c.decoder.Decode(&env); err != nil {
			return 0, fmt.Errorf("读取鉴权结果失败: %w", err)
		}
		if env.Type == MsgLog {
			continue
		}
		if env.Type != MsgAuth {
			continue
		}
		var ap authPayload
		_ = json.Unmarshal(env.Payload, &ap)
		if ap.Status != "" && ap.Status != "ok" {
			return 0, fmt.Errorf("鉴权失败 status=%s", ap.Status)
		}
		break
	}
	_ = c.conn.SetReadDeadline(time.Time{})
	log.Printf("[R%03d][C%02d][控制] 注册+鉴权成功", c.round, c.id)
	return time.Since(start), nil
}

func (c *ControlClient) startListening(ctx context.Context) {
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			default:
			}

			var env rawEnvelope
			if err := c.decoder.Decode(&env); err != nil {
				if !isNetClosed(err) {
					log.Printf("[R%03d][C%02d][控制] 读取消息失败 err=%v", c.round, c.id, err)
				}
				return
			}

			switch env.Type {
			case MsgPortAlloc:
				var p PortAllocPayload
				if err := json.Unmarshal(env.Payload, &p); err == nil {
					select {
					case c.portAllocCh <- p:
					default:
					}
					log.Printf("[R%03d][C%02d][控制] 收到端口分配 zmq=[%d-%d] srt=[%d-%d]",
						c.round, c.id, p.ZMQStartPort, p.ZMQEndPort, p.SRTStartPort, p.SRTEndPort)
				} else {
					log.Printf("[R%03d][C%02d][控制] 端口分配解析失败 payload=%s err=%v", c.round, c.id, string(env.Payload), err)
				}
			case MsgPortGrant:
				var g PortGrantPayload
				if err := json.Unmarshal(env.Payload, &g); err == nil {
					select {
					case c.portGrantCh <- g:
					default:
					}
				}
			case MsgPortState:
				var s PortStatePayload
				if err := json.Unmarshal(env.Payload, &s); err == nil {
					select {
					case c.portStateCh <- s:
					default:
					}
				}
			case MsgKeyExchange:
				var p KeyExchangePayload
				if err := json.Unmarshal(env.Payload, &p); err == nil {
					select {
					case c.keyExchangeCh <- p:
					default:
					}
					log.Printf("[R%03d][C%02d][控制] 收到密钥交换 public_key_len=%d", c.round, c.id, len(p.PublicKey))
				} else {
					log.Printf("[R%03d][C%02d][控制] 密钥交换解析失败 payload=%s err=%v", c.round, c.id, string(env.Payload), err)
				}
			case MsgTelemetry:
				var t TelemetryPayload
				if err := json.Unmarshal(env.Payload, &t); err == nil && t.CurrentSpeed > 0 {
					log.Printf("[R%03d][C%02d][遥测] current_speed=%.2fMbps", c.round, c.id, t.CurrentSpeed)
				}
			case MsgLog:
				log.Printf("[R%03d][C%02d][控制][LOG] %s", c.round, c.id, strings.TrimSpace(decodeLog(env.Payload)))
			case MsgPong:
				var clock ClockPayload
				if err := json.Unmarshal(env.Payload, &clock); err == nil && clock.ClientSendTime > 0 {
					now := time.Now().UnixNano()
					rttMs := float64(now-clock.ClientSendTime) / 1e6
					c.lastPingRTTMs.Store(int64(math.Round(rttMs * 1000)))
					select {
					case c.pongCh <- clock:
					default:
					}
					log.Printf("[R%03d][C%02d][控制][PING] rtt=%.2fms", c.round, c.id, rttMs)
				} else {
					log.Printf("[R%03d][C%02d][控制] pong payload=%s", c.round, c.id, strings.TrimSpace(string(env.Payload)))
				}
			default:
				log.Printf("[R%03d][C%02d][控制] 收到消息 type=%s payload=%s", c.round, c.id, env.Type, strings.TrimSpace(string(env.Payload)))
			}
		}
	}()
}

func (c *ControlClient) startPingLoop(ctx context.Context, interval time.Duration) {
	go func() {
		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				sendNs := time.Now().UnixNano()
				atomic.StoreInt64(&c.lastPingSendNs, sendNs)
				err := c.send(BaseMessage{
					Type: MsgPing,
					Payload: ClockPayload{
						ClientSendTime: sendNs,
						ServerRecvTime: 0,
						ServerSendTime: 0,
					},
				})
				if err != nil {
					log.Printf("[R%03d][C%02d][控制] 发送Ping失败 err=%v", c.round, c.id, err)
					return
				}
			}
		}
	}()
}

type DataConn struct {
	proto string
	port  int
	conn  net.Conn
}

func dialData(ctx context.Context, proto, host string, port int) (*DataConn, time.Duration, error) {
	start := time.Now()
	d := net.Dialer{}
	conn, err := d.DialContext(ctx, "tcp", fmt.Sprintf("%s:%d", host, port))
	if err != nil {
		return nil, 0, err
	}
	h, _ := json.Marshal(map[string]string{
		"role":      "FE",
		"client_id": fmt.Sprintf("fe-%s-%d", proto, port),
	})
	_, _ = conn.Write(append(append([]byte("RLX1HELLO "), h...), '\n'))
	return &DataConn{proto: proto, port: port, conn: conn}, time.Since(start), nil
}

func (c *DataConn) close() {
	if c != nil && c.conn != nil {
		_ = c.conn.Close()
	}
}

func (c *DataConn) writeAllWithDeadline(b []byte, d time.Duration) error {
	if err := c.conn.SetWriteDeadline(time.Now().Add(d)); err != nil {
		return err
	}
	_, err := c.conn.Write(b)
	return err
}

func (c *DataConn) readFullWithDeadline(dst []byte, d time.Duration) (int, error) {
	if err := c.conn.SetReadDeadline(time.Now().Add(d)); err != nil {
		return 0, err
	}
	return io.ReadFull(c.conn, dst)
}

type SessionMetrics struct {
	ControlConnectDur time.Duration
	RegisterAuthDur   time.Duration
	WaitPortAllocDur  time.Duration
	WaitPortGrantDur  time.Duration
	WaitKeyExDur      time.Duration
	ZMQConnectDur     time.Duration
	SRTConnectDur     time.Duration
	ZMQCtrlRTT        time.Duration
	VideoSendDur      time.Duration
	VideoBytesSent    int64
	VideoBytesEcho    int64
	VideoSHA256       string
	ZMQPort           int
	SRTPort           int
	PortSource        string
}

type SessionResult struct {
	Round    int
	ClientID int
	Success  bool
	Stage    string
	Err      string
	Metrics  SessionMetrics
}

type FullFlowConfig struct {
	GatewayAddr   string
	AuthSecret    string
	VideoPath     string
	Rounds        int
	Parallel      int
	PingInterval  time.Duration
	StageTimeout  time.Duration
	DataDialRetry int
	DataDialDelay time.Duration
	VideoLimitMB  int64
	BurstKB       int
	BurstDelay    time.Duration
	BurstJitter   time.Duration
	DataHost      string
}

func runFullFlow(cfg FullFlowConfig) []SessionResult {
	totalSessions := cfg.Rounds * cfg.Parallel
	results := make([]SessionResult, 0, totalSessions)
	var resultsMu sync.Mutex

	for r := 0; r < cfg.Rounds; r++ {
		log.Printf("[R%03d][主控] 开始 round=%d/%d parallel=%d", r, r+1, cfg.Rounds, cfg.Parallel)

		readyCh := make(chan struct{}, cfg.Parallel)
		startCh := make(chan struct{})

		var wg sync.WaitGroup
		wg.Add(cfg.Parallel)
		perRound := make([]SessionResult, cfg.Parallel)

		for i := 0; i < cfg.Parallel; i++ {
			idx := i
			go func() {
				defer wg.Done()
				res := runOneSession(cfg, r, idx, readyCh, startCh)
				perRound[idx] = res
			}()
		}

		barrierTimeout := 10 * time.Second
		if cfg.StageTimeout > 0 && cfg.StageTimeout/2 < barrierTimeout {
			barrierTimeout = cfg.StageTimeout / 2
		}

		want := cfg.Parallel
		got := 0
		deadline := time.NewTimer(barrierTimeout)
		for got < want {
			select {
			case <-readyCh:
				got++
			case <-deadline.C:
				log.Printf("[R%03d][主控] 同步屏障超时 got=%d want=%d，已就绪的会话将直接开始发送视频", r, got, want)
				got = want
			}
		}
		deadline.Stop()
		close(startCh)

		wg.Wait()

		resultsMu.Lock()
		results = append(results, perRound...)
		resultsMu.Unlock()
	}

	return results
}

func runOneSession(cfg FullFlowConfig, round, clientID int, readyCh chan<- struct{}, startCh <-chan struct{}) SessionResult {
	res := SessionResult{
		Round:    round,
		ClientID: clientID,
		Success:  false,
	}
	sessionStart := time.Now()
	defer func() {
		log.Printf("[R%03d][C%02d][会话] 结束 success=%t stage=%s dur=%s err=%s reg_auth=%s port_wait=%s zmq_rtt=%s video_sent=%dB video_echo=%dB",
			round, clientID, res.Success, res.Stage, time.Since(sessionStart), strings.TrimSpace(res.Err),
			res.Metrics.RegisterAuthDur, res.Metrics.WaitPortAllocDur, res.Metrics.ZMQCtrlRTT,
			res.Metrics.VideoBytesSent, res.Metrics.VideoBytesEcho)
	}()

	ctx, cancel := context.WithTimeout(context.Background(), cfg.StageTimeout)
	defer cancel()

	cc := newControlClient(clientID, round, cfg.GatewayAddr, cfg.AuthSecret)
	defer cc.close()

	t0 := time.Now()
	if err := cc.connect(ctx); err != nil {
		res.Stage = "control_connect"
		res.Err = err.Error()
		return res
	}
	res.Metrics.ControlConnectDur = time.Since(t0)

	authDur, err := cc.registerAndAuth(ctx)
	if err != nil {
		res.Stage = "register_auth"
		res.Err = err.Error()
		return res
	}
	res.Metrics.RegisterAuthDur = authDur

	listenCtx, listenCancel := context.WithCancel(context.Background())
	defer listenCancel()
	cc.startListening(listenCtx)
	cc.startPingLoop(listenCtx, cfg.PingInterval)

	waitStart := time.Now()
	var portAlloc PortAllocPayload
	select {
	case portAlloc = <-cc.portAllocCh:
		res.Metrics.WaitPortAllocDur = time.Since(waitStart)
	case <-ctx.Done():
		res.Stage = "wait_port_alloc"
		res.Err = "等待端口分配超时"
		return res
	}

	waitKeyStart := time.Now()
	select {
	case <-cc.keyExchangeCh:
		res.Metrics.WaitKeyExDur = time.Since(waitKeyStart)
	case <-time.After(2 * time.Second):
		res.Metrics.WaitKeyExDur = time.Since(waitKeyStart)
		log.Printf("[R%03d][C%02d][控制] 未在2s内收到密钥交换，继续执行", round, clientID)
	}

	zmqPort, srtPort, portSource, waitGrantDur, err := acquirePortsOrPick(ctx, cc, portAlloc, round, clientID, cfg.Parallel)
	res.Metrics.WaitPortGrantDur = waitGrantDur
	if err != nil {
		res.Stage = "pick_ports"
		res.Err = err.Error()
		return res
	}
	res.Metrics.ZMQPort = zmqPort
	res.Metrics.SRTPort = srtPort
	res.Metrics.PortSource = portSource
	log.Printf("[R%03d][C%02d][数据] 选择端口 source=%s zmq=%d srt=%d", round, clientID, portSource, zmqPort, srtPort)

	zmq, zmqDur, err := dialWithRetry(ctx, "zmq", cfg.DataHost, zmqPort, cfg.DataDialRetry, cfg.DataDialDelay)
	if err != nil {
		res.Stage = "dial_zmq"
		res.Err = err.Error()
		return res
	}
	res.Metrics.ZMQConnectDur = zmqDur
	defer zmq.close()
	log.Printf("[R%03d][C%02d][ZMQ-%d] 已连接 dial_dur=%s", round, clientID, zmqPort, zmqDur)

	srt, srtDur, err := dialWithRetry(ctx, "srt", cfg.DataHost, srtPort, cfg.DataDialRetry, cfg.DataDialDelay)
	if err != nil {
		res.Stage = "dial_srt"
		res.Err = err.Error()
		return res
	}
	res.Metrics.SRTConnectDur = srtDur
	defer srt.close()
	log.Printf("[R%03d][C%02d][SRT-%d] 已连接 dial_dur=%s", round, clientID, srtPort, srtDur)

	select {
	case readyCh <- struct{}{}:
	default:
	}

	ctrlRTT, err := sendZMQControlAndWaitEcho(ctx, zmq, round, clientID)
	if err != nil {
		log.Printf("[R%03d][C%02d][ZMQ-%d] 控制RTT测试失败(忽略继续发视频): %v", round, clientID, zmqPort, err)
	} else {
		res.Metrics.ZMQCtrlRTT = ctrlRTT
	}

	select {
	case <-startCh:
	case <-ctx.Done():
		res.Stage = "sync_start"
		res.Err = "等待同步开始超时"
		return res
	}

	rng := rand.New(rand.NewSource(time.Now().UnixNano() + int64(round*1000+clientID)))
	log.Printf("[R%03d][C%02d][SRT-%d] 抖动模拟 burst_kb=%d delay=%s jitter=%s",
		round, clientID, srtPort, cfg.BurstKB, cfg.BurstDelay, cfg.BurstJitter)
	videoBytes, echoBytes, sha, sendDur, err := sendSRTVideoAndOptionallyEcho(ctx, srt, cfg.VideoPath, cfg.VideoLimitMB, cfg.BurstKB, cfg.BurstDelay, cfg.BurstJitter, rng)
	if err != nil {
		res.Stage = "srt_video"
		res.Err = err.Error()
		return res
	}
	res.Metrics.VideoSendDur = sendDur
	res.Metrics.VideoBytesSent = videoBytes
	res.Metrics.VideoBytesEcho = echoBytes
	res.Metrics.VideoSHA256 = sha

	mbps := 0.0
	if sendDur > 0 {
		mbps = float64(videoBytes*8) / 1e6 / sendDur.Seconds()
	}
	echoRatio := 0.0
	if videoBytes > 0 {
		echoRatio = float64(echoBytes) / float64(videoBytes)
	}
	log.Printf("[R%03d][C%02d][SRT-%d] 视频发送完成 bytes=%d echo=%d echo_ratio=%.2f dur=%s mbps=%.2f sha256=%s",
		round, clientID, srtPort, videoBytes, echoBytes, echoRatio, sendDur, mbps, shortSHA(sha))

	res.Success = true
	res.Stage = "ok"
	return res
}

func dialWithRetry(ctx context.Context, proto, host string, port int, attempts int, delay time.Duration) (*DataConn, time.Duration, error) {
	var last error
	var total time.Duration
	for i := 0; i < attempts; i++ {
		dc, dur, err := dialData(ctx, proto, host, port)
		total += dur
		if err == nil {
			_ = dc.conn.SetDeadline(time.Time{})
			return dc, total, nil
		}
		last = err
		select {
		case <-ctx.Done():
			return nil, total, fmt.Errorf("连接%s端口超时 port=%d last=%v", proto, port, last)
		case <-time.After(delay):
		}
	}
	return nil, total, fmt.Errorf("连接%s端口失败 port=%d attempts=%d last=%v", proto, port, attempts, last)
}

func pickPorts(p PortAllocPayload, round, clientID, parallel int) (int, int, error) {
	zmqCount := p.ZMQEndPort - p.ZMQStartPort + 1
	srtCount := p.SRTEndPort - p.SRTStartPort + 1
	if zmqCount <= 0 || srtCount <= 0 {
		return 0, 0, fmt.Errorf("端口范围非法 zmq=[%d-%d] srt=[%d-%d]", p.ZMQStartPort, p.ZMQEndPort, p.SRTStartPort, p.SRTEndPort)
	}

	offset := clientID
	if offset >= zmqCount || offset >= srtCount {
		return 0, 0, fmt.Errorf("端口不够用于rounds并行：需要offset=%d，zmq_count=%d srt_count=%d", offset, zmqCount, srtCount)
	}
	return p.ZMQStartPort + offset, p.SRTStartPort + offset, nil
}

func acquirePortsOrPick(ctx context.Context, cc *ControlClient, p PortAllocPayload, round, clientID, parallel int) (int, int, string, time.Duration, error) {
	start := time.Now()
	offset := clientID
	_ = cc.send(BaseMessage{Type: MsgPortStateQ, Payload: PortStateQueryPayload{NeedZMQ: true, NeedSRT: true}})
	stateDeadline := time.NewTimer(3 * time.Second)
	defer stateDeadline.Stop()
	var lastState PortStatePayload
	receivedState := false
	for {
		select {
		case <-ctx.Done():
			if receivedState {
				return 0, 0, "", time.Since(start), fmt.Errorf("已收到端口状态但就绪端口不足 offset=%d zmq_ready=%d srt_ready=%d: %w",
					offset, len(lastState.ZMQReadyPorts), len(lastState.SRTReadyPorts), ctx.Err())
			}
			return 0, 0, "", time.Since(start), fmt.Errorf("等待端口状态超时/取消: %w", ctx.Err())
		case <-stateDeadline.C:
			goto fallbackGrant
		case s := <-cc.portStateCh:
			receivedState = true
			lastState = s
			if offset < len(s.ZMQReadyPorts) && offset < len(s.SRTReadyPorts) {
				return s.ZMQReadyPorts[offset], s.SRTReadyPorts[offset], "port_state", time.Since(start), nil
			}
		}
	}

fallbackGrant:
	zmqReqID := fmt.Sprintf("r%03d-c%02d-zmq", round, clientID)
	srtReqID := fmt.Sprintf("r%03d-c%02d-srt", round, clientID)
	_ = cc.send(BaseMessage{Type: MsgPortAcquire, Payload: PortAcquirePayload{RequestID: zmqReqID, NeedZMQ: true, NeedSRT: false, TTLSeconds: 60}})
	_ = cc.send(BaseMessage{Type: MsgPortAcquire, Payload: PortAcquirePayload{RequestID: srtReqID, NeedZMQ: false, NeedSRT: true, TTLSeconds: 60}})

	var zmqPort, srtPort int
	grantDeadline := time.NewTimer(3 * time.Second)
	defer grantDeadline.Stop()
	for zmqPort == 0 || srtPort == 0 {
		select {
		case <-ctx.Done():
			if receivedState {
				return 0, 0, "", time.Since(start), fmt.Errorf("已收到端口状态但就绪端口不足 offset=%d zmq_ready=%d srt_ready=%d: %w",
					offset, len(lastState.ZMQReadyPorts), len(lastState.SRTReadyPorts), ctx.Err())
			}
			return 0, 0, "", time.Since(start), fmt.Errorf("等待端口租用超时/取消: %w", ctx.Err())
		case <-grantDeadline.C:
			if receivedState {
				return 0, 0, "", time.Since(start), fmt.Errorf("就绪端口不足 offset=%d zmq_ready=%d srt_ready=%d",
					offset, len(lastState.ZMQReadyPorts), len(lastState.SRTReadyPorts))
			}
			return 0, 0, "", time.Since(start), fmt.Errorf("等待端口状态/租用超时：未收到port_state且未获得port_grant")
		case g := <-cc.portGrantCh:
			if g.Error != "" {
				continue
			}
			if g.RequestID == zmqReqID && g.ZMQPort > 0 {
				zmqPort = g.ZMQPort
			}
			if g.RequestID == srtReqID && g.SRTPort > 0 {
				srtPort = g.SRTPort
			}
		}
	}
	return zmqPort, srtPort, "port_grant", time.Since(start), nil
}

func sendZMQControlAndWaitEcho(ctx context.Context, zmq *DataConn, round, clientID int) (time.Duration, error) {
	const samples = 5
	var total time.Duration
	for i := 0; i < samples; i++ {
		payload := map[string]any{
			"type":      "zmq_control",
			"round":     round,
			"client_id": clientID,
			"seq":       i,
			"ts_ns":     time.Now().UnixNano(),
		}
		b, _ := json.Marshal(payload)
		msg := append([]byte("ZCTRL:"), b...)
		msg = append(msg, '\n')

		start := time.Now()
		if err := zmq.writeAllWithDeadline(msg, 2*time.Second); err != nil {
			return 0, fmt.Errorf("发送ZMQ控制失败: %w", err)
		}

		echo := make([]byte, len(msg))
		n, err := zmq.readFullWithDeadline(echo, 2*time.Second)
		if err != nil {
			return 0, fmt.Errorf("读取ZMQ控制回包失败: %w", err)
		}
		if n != len(msg) || string(echo) != string(msg) {
			return 0, fmt.Errorf("ZMQ控制回包不一致 n=%d", n)
		}
		total += time.Since(start)

		if err := ctx.Err(); err != nil {
			return 0, fmt.Errorf("ZMQ控制测试超时/取消: %w", err)
		}
	}
	avg := total / samples
	log.Printf("[R%03d][C%02d][ZMQ-%d] 控制消息往返 avg_rtt=%s samples=%d", round, clientID, zmq.port, avg, samples)
	return avg, nil
}

func sendSRTVideoAndOptionallyEcho(ctx context.Context, srt *DataConn, videoPath string, limitMB int64, burstKB int, burstDelay, burstJitter time.Duration, rng *rand.Rand) (int64, int64, string, time.Duration, error) {
	f, err := os.Open(videoPath)
	if err != nil {
		return 0, 0, "", 0, fmt.Errorf("打开视频失败: %w", err)
	}
	defer f.Close()

	var limit int64
	if limitMB > 0 {
		limit = limitMB * 1024 * 1024
	}

	hasher := sha256.New()
	buf := make([]byte, 32*1024)

	var sent int64
	var echoRead atomic.Int64
	echoStop := make(chan struct{})
	echoErr := make(chan error, 1)

	burstBytes := int64(burstKB) * 1024
	if burstBytes <= 0 {
		burstBytes = 256 * 1024
	}
	if rng == nil {
		rng = rand.New(rand.NewSource(time.Now().UnixNano()))
	}
	var burstAcc int64

	go func() {
		defer close(echoErr)
		tmp := make([]byte, 32*1024)
		for {
			select {
			case <-echoStop:
				return
			default:
			}

			_ = srt.conn.SetReadDeadline(time.Now().Add(800 * time.Millisecond))
			n, err := srt.conn.Read(tmp)
			if n > 0 {
				echoRead.Add(int64(n))
			}
			if err != nil {
				if ne, ok := err.(net.Error); ok && ne.Timeout() {
					continue
				}
				if isNetClosed(err) {
					return
				}
				echoErr <- err
				return
			}
		}
	}()

	start := time.Now()
	for {
		if err := ctx.Err(); err != nil {
			close(echoStop)
			<-echoErr
			return sent, echoRead.Load(), "", time.Since(start), fmt.Errorf("发送视频超时/取消: %w", err)
		}

		if limit > 0 && sent >= limit {
			break
		}

		maxRead := len(buf)
		if limit > 0 {
			remain := limit - sent
			if remain < int64(maxRead) {
				maxRead = int(remain)
			}
		}

		n, rerr := f.Read(buf[:maxRead])
		if n > 0 {
			_, _ = hasher.Write(buf[:n])
			if err := srt.conn.SetWriteDeadline(time.Now().Add(2 * time.Second)); err != nil {
				close(echoStop)
				<-echoErr
				return sent, echoRead.Load(), "", time.Since(start), fmt.Errorf("设置写超时失败: %w", err)
			}
			wn, werr := srt.conn.Write(buf[:n])
			if werr != nil {
				close(echoStop)
				<-echoErr
				return sent, echoRead.Load(), "", time.Since(start), fmt.Errorf("发送视频失败: %w", werr)
			}
			sent += int64(wn)
			if wn != n {
				close(echoStop)
				<-echoErr
				return sent, echoRead.Load(), "", time.Since(start), fmt.Errorf("短写入 wn=%d n=%d", wn, n)
			}

			burstAcc += int64(wn)
			if burstAcc >= burstBytes {
				burstAcc = 0
				sleep := burstDelay
				if burstJitter > 0 {
					sleep += time.Duration(rng.Int63n(int64(burstJitter) + 1))
				}
				if sleep > 0 {
					select {
					case <-ctx.Done():
						close(echoStop)
						<-echoErr
						return sent, echoRead.Load(), "", time.Since(start), fmt.Errorf("发送视频超时/取消: %w", ctx.Err())
					case <-time.After(sleep):
					}
				}
			}
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			close(echoStop)
			<-echoErr
			return sent, echoRead.Load(), "", time.Since(start), fmt.Errorf("读取视频失败: %w", rerr)
		}
	}

	dur := time.Since(start)
	close(echoStop)
	if err := <-echoErr; err != nil {
		return sent, echoRead.Load(), "", dur, fmt.Errorf("读取回传失败: %w", err)
	}

	sum := hex.EncodeToString(hasher.Sum(nil))
	return sent, echoRead.Load(), sum, dur, nil
}

func isNetClosed(err error) bool {
	if err == nil {
		return false
	}
	if errorsIs(err, net.ErrClosed) {
		return true
	}
	s := strings.ToLower(err.Error())
	return strings.Contains(s, "use of closed network connection") || strings.Contains(s, "closed pipe")
}

func errorsIs(err, target error) bool {
	type causer interface{ Unwrap() error }
	for err != nil {
		if err == target {
			return true
		}
		u, ok := err.(causer)
		if !ok {
			return false
		}
		err = u.Unwrap()
	}
	return false
}

func decodeLog(payload []byte) string {
	if len(payload) == 0 {
		return ""
	}
	if payload[0] == '"' {
		var s string
		if err := json.Unmarshal(payload, &s); err == nil {
			return s
		}
	}
	return string(payload)
}

type Report struct {
	Total        int
	Success      int
	Fail         int
	FailByStage  map[string]int
	AvgRegAuthMs float64
	AvgPortMs    float64
	AvgGrantMs   float64
	AvgZmqRTTMs  float64
	AvgSrtDialMs float64
	AvgVideoMbps float64
	P50VideoMbps float64
	P95VideoMbps float64
	AvgEchoRatio float64
	P50EchoRatio float64
	P95EchoRatio float64
}

func buildReport(results []SessionResult) Report {
	r := Report{
		Total:       len(results),
		FailByStage: make(map[string]int),
	}

	var regAuth []float64
	var portWait []float64
	var grantWait []float64
	var zmqRTT []float64
	var srtDial []float64
	var videoMbps []float64
	var echoRatio []float64

	for _, s := range results {
		if s.Success {
			r.Success++
		} else {
			r.Fail++
			stage := s.Stage
			if stage == "" {
				stage = "unknown"
			}
			r.FailByStage[stage]++
			continue
		}

		regAuth = append(regAuth, float64(s.Metrics.RegisterAuthDur)/1e6)
		portWait = append(portWait, float64(s.Metrics.WaitPortAllocDur)/1e6)
		grantWait = append(grantWait, float64(s.Metrics.WaitPortGrantDur)/1e6)
		zmqRTT = append(zmqRTT, float64(s.Metrics.ZMQCtrlRTT)/1e6)
		srtDial = append(srtDial, float64(s.Metrics.SRTConnectDur)/1e6)
		if s.Metrics.VideoSendDur > 0 && s.Metrics.VideoBytesSent > 0 {
			mbps := float64(s.Metrics.VideoBytesSent*8) / 1e6 / s.Metrics.VideoSendDur.Seconds()
			videoMbps = append(videoMbps, mbps)
			echoRatio = append(echoRatio, float64(s.Metrics.VideoBytesEcho)/float64(s.Metrics.VideoBytesSent))
		}
	}

	r.AvgRegAuthMs = avg(regAuth)
	r.AvgPortMs = avg(portWait)
	r.AvgGrantMs = avg(grantWait)
	r.AvgZmqRTTMs = avg(zmqRTT)
	r.AvgSrtDialMs = avg(srtDial)
	r.AvgVideoMbps = avg(videoMbps)
	r.P50VideoMbps = percentile(videoMbps, 0.50)
	r.P95VideoMbps = percentile(videoMbps, 0.95)
	r.AvgEchoRatio = avg(echoRatio)
	r.P50EchoRatio = percentile(echoRatio, 0.50)
	r.P95EchoRatio = percentile(echoRatio, 0.95)
	return r
}

func avg(v []float64) float64 {
	if len(v) == 0 {
		return 0
	}
	var s float64
	for _, x := range v {
		s += x
	}
	return s / float64(len(v))
}

func percentile(v []float64, p float64) float64 {
	if len(v) == 0 {
		return 0
	}
	cp := append([]float64(nil), v...)
	sort.Float64s(cp)
	if p <= 0 {
		return cp[0]
	}
	if p >= 1 {
		return cp[len(cp)-1]
	}
	pos := p * float64(len(cp)-1)
	i := int(pos)
	f := pos - float64(i)
	if i+1 >= len(cp) {
		return cp[len(cp)-1]
	}
	return cp[i]*(1-f) + cp[i+1]*f
}

func printReport(cfg FullFlowConfig, results []SessionResult) {
	rep := buildReport(results)

	fmt.Println()
	fmt.Println("========== 分析报告 ==========")
	fmt.Printf("模式: full-flow (FE)\n")
	fmt.Printf("控制面: %s\n", cfg.GatewayAddr)
	fmt.Printf("数据面: %s (端口来自port_grant，超时回退port_alloc)\n", cfg.DataHost)
	fmt.Printf("视频: %s (limit=%dMB)\n", cfg.VideoPath, cfg.VideoLimitMB)
	fmt.Printf("抖动模拟: burst_kb=%d delay=%s jitter=%s\n", cfg.BurstKB, cfg.BurstDelay, cfg.BurstJitter)
	fmt.Printf("轮次: %d, 并行连接: %d, 会话总数: %d\n", cfg.Rounds, cfg.Parallel, rep.Total)
	fmt.Println()
	fmt.Printf("成功: %d, 失败: %d, 成功率: %.2f%%\n", rep.Success, rep.Fail, float64(rep.Success)/float64(max(1, rep.Total))*100)
	fmt.Printf("平均 注册+鉴权: %.1fms\n", rep.AvgRegAuthMs)
	fmt.Printf("平均 等待端口分配: %.1fms\n", rep.AvgPortMs)
	fmt.Printf("平均 等待端口租用: %.1fms\n", rep.AvgGrantMs)
	fmt.Printf("平均 ZMQ控制RTT: %.1fms\n", rep.AvgZmqRTTMs)
	fmt.Printf("平均 SRT连接耗时: %.1fms\n", rep.AvgSrtDialMs)
	fmt.Printf("视频吞吐: avg=%.2fMbps p50=%.2fMbps p95=%.2fMbps\n", rep.AvgVideoMbps, rep.P50VideoMbps, rep.P95VideoMbps)
	fmt.Printf("SRT回传比: avg=%.2f p50=%.2f p95=%.2f\n", rep.AvgEchoRatio, rep.P50EchoRatio, rep.P95EchoRatio)

	if rep.Fail > 0 {
		fmt.Println()
		fmt.Println("失败分布(按阶段):")
		type kv struct {
			k string
			v int
		}
		var a []kv
		for k, v := range rep.FailByStage {
			a = append(a, kv{k: k, v: v})
		}
		sort.Slice(a, func(i, j int) bool {
			if a[i].v == a[j].v {
				return a[i].k < a[j].k
			}
			return a[i].v > a[j].v
		})
		for _, x := range a {
			fmt.Printf("- %s: %d\n", x.k, x.v)
		}
	}

	fmt.Println()
	fmt.Println("样例明细(前20条):")
	for i := 0; i < len(results) && i < 20; i++ {
		s := results[i]
		videoMbps := 0.0
		if s.Metrics.VideoSendDur > 0 {
			videoMbps = float64(s.Metrics.VideoBytesSent*8) / 1e6 / s.Metrics.VideoSendDur.Seconds()
		}
		echoRatio := 0.0
		if s.Metrics.VideoBytesSent > 0 {
			echoRatio = float64(s.Metrics.VideoBytesEcho) / float64(s.Metrics.VideoBytesSent)
		}
		fmt.Printf("- R%03d C%02d success=%t stage=%s src=%s zmq=%d srt=%d reg_auth=%s port_wait=%s grant_wait=%s zmq_rtt=%s srt_dial=%s video_mbps=%.2f video_sent=%dB video_echo=%dB echo_ratio=%.2f video_dur=%s sha256=%s err=%s\n",
			s.Round, s.ClientID, s.Success, s.Stage,
			s.Metrics.PortSource, s.Metrics.ZMQPort, s.Metrics.SRTPort,
			s.Metrics.RegisterAuthDur, s.Metrics.WaitPortAllocDur, s.Metrics.WaitPortGrantDur, s.Metrics.ZMQCtrlRTT, s.Metrics.SRTConnectDur,
			videoMbps, s.Metrics.VideoBytesSent, s.Metrics.VideoBytesEcho, echoRatio, s.Metrics.VideoSendDur,
			shortSHA(s.Metrics.VideoSHA256), s.Err)
	}
	fmt.Println("========== 报告结束 ==========")
}

func shortSHA(s string) string {
	if len(s) <= 12 {
		return s
	}
	return s[:12]
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func main() {
	var cfg FullFlowConfig
	flag.StringVar(&cfg.GatewayAddr, "gateway", "100.115.247.57:5555", "控制面地址 host:port")
	flag.StringVar(&cfg.AuthSecret, "secret", "dummy_secret", "鉴权密钥")
	flag.StringVar(&cfg.VideoPath, "video", "./IMG_4127.MOV", "视频文件路径")
	flag.IntVar(&cfg.Rounds, "rounds", 1, "执行轮次(每轮2个并行连接)")
	flag.IntVar(&cfg.Parallel, "parallel", 2, "并行连接数(推荐2)")
	flag.DurationVar(&cfg.PingInterval, "ping", 1*time.Second, "控制面Ping间隔")
	flag.DurationVar(&cfg.StageTimeout, "timeout", 45*time.Second, "单会话超时")
	flag.IntVar(&cfg.DataDialRetry, "dial_retry", 60, "数据面连接重试次数")
	flag.DurationVar(&cfg.DataDialDelay, "dial_delay", 100*time.Millisecond, "数据面连接重试间隔")
	flag.Int64Var(&cfg.VideoLimitMB, "video_mb", 0, "发送视频大小上限(MB)，0表示全量(默认全量发送IMG_4127.MOV)")
	flag.IntVar(&cfg.BurstKB, "burst_kb", 256, "抖动模拟：每发送多少KB后sleep一次")
	flag.DurationVar(&cfg.BurstDelay, "delay", 10*time.Millisecond, "抖动模拟：每个burst固定延迟")
	flag.DurationVar(&cfg.BurstJitter, "jitter", 30*time.Millisecond, "抖动模拟：每个burst附加随机抖动(0~jitter)")
	flag.StringVar(&cfg.DataHost, "data_host", "100.115.247.57", "数据面host(端口来自port_grant，超时回退port_alloc)")
	flag.Parse()

	if cfg.Rounds <= 0 {
		log.Fatalf("rounds必须>0")
	}
	if cfg.Parallel <= 0 {
		log.Fatalf("parallel必须>0")
	}

	if _, err := os.Stat(cfg.VideoPath); err != nil {
		log.Fatalf("视频文件不可用: %v", err)
	}

	log.SetFlags(log.LstdFlags | log.Lmicroseconds)
	log.Printf("[主控] client_test full-flow 启动 gateway=%s rounds=%d parallel=%d timeout=%s video=%s video_mb=%d burst_kb=%d delay=%s jitter=%s",
		cfg.GatewayAddr, cfg.Rounds, cfg.Parallel, cfg.StageTimeout, cfg.VideoPath, cfg.VideoLimitMB, cfg.BurstKB, cfg.BurstDelay, cfg.BurstJitter)
	log.Printf("[主控] 说明: 该程序作为FE测试端到端链路；需有BE已连接并发送port_alloc/可建立数据面配对")

	results := runFullFlow(cfg)
	printReport(cfg, results)
}
