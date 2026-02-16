package relay

import (
	"context"
	"fmt"
	"net"
	"sync"
	"time"

	"relay-x/v4/config"
	v4errors "relay-x/v4/errors"
	v4log "relay-x/v4/log"
	"relay-x/v4/ports"
)

type tcpServer struct {
	kind ports.Kind
	pool *ports.Pool
	zmq  config.ZeroMQConfig

	mu        sync.RWMutex
	listeners map[int]net.Listener
	pendingS  map[int]*BufferedConn
	pendingC  map[int]*BufferedConn
	sessions  map[int]*Session

	onSessionStop func(port int, sessionID string)

	maintOnce sync.Once
}

// newTCPServer 创建 TCP 数据面服务（用于 ZMQ/TCP 端口）。
// 参数：
// - kind: 端口类型（通常为 zmq）
// - pool: 端口池（用于校验 sessionID 并推进端口状态）
// - zmq: ZMQ 相关 socket 参数（用于对 TCPConn 做基础优化配置）
func newTCPServer(kind ports.Kind, pool *ports.Pool, zmq config.ZeroMQConfig) *tcpServer {
	return &tcpServer{
		kind:      kind,
		pool:      pool,
		zmq:       zmq,
		listeners: make(map[int]net.Listener),
		pendingS:  make(map[int]*BufferedConn),
		pendingC:  make(map[int]*BufferedConn),
		sessions:  make(map[int]*Session),
	}
}

// StartPort 在指定端口启动 TCP 监听（幂等）。
// 参数：
// - ctx: 上下文（用于配对/会话生命周期联动）
// - port: 监听端口
// 返回：
// - error: 监听失败原因
func (s *tcpServer) StartPort(ctx context.Context, port int) error {
	s.maintOnce.Do(func() { go s.pendingMaintenanceLoop(ctx) })

	s.mu.Lock()
	if _, ok := s.listeners[port]; ok {
		s.mu.Unlock()
		return nil
	}
	s.mu.Unlock()

	ln, err := net.Listen("tcp", fmt.Sprintf("0.0.0.0:%d", port))
	if err != nil {
		return v4errors.Wrap(v4errors.CodeConflict, "tcp listen failed", err)
	}

	s.mu.Lock()
	if _, ok := s.listeners[port]; ok {
		s.mu.Unlock()
		_ = ln.Close()
		return nil
	}
	s.listeners[port] = ln
	s.mu.Unlock()

	v4log.With(map[string]any{"port": port, "status": "listen_ok"}).Info("数据端口开始监听")
	go s.acceptLoop(ctx, port, ln)
	return nil
}

// StopAll 关闭所有监听、挂起连接与已建立会话（幂等）。
func (s *tcpServer) StopAll() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for port, ln := range s.listeners {
		_ = ln.Close()
		delete(s.listeners, port)
	}
	for port, p := range s.pendingS {
		_ = p.Close()
		delete(s.pendingS, port)
	}
	for port, p := range s.pendingC {
		_ = p.Close()
		delete(s.pendingC, port)
	}
	for port, sess := range s.sessions {
		sess.Stop()
		delete(s.sessions, port)
	}
}

// acceptLoop 接受新连接并交给 handleConn 处理。
func (s *tcpServer) acceptLoop(ctx context.Context, port int, ln net.Listener) {
	for {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		go s.handleConn(ctx, port, c)
	}
}

// handleConn 处理数据面连接：
// - 读取 V4HELLO 握手
// - 校验端口池 allocation/sessionID
// - 依据 role 放入 pendingS/pendingC 并尝试配对
func (s *tcpServer) handleConn(ctx context.Context, port int, c net.Conn) {
	if tc, ok := c.(*net.TCPConn); ok && s.kind == ports.KindZMQ {
		_ = tc.SetNoDelay(true)
		if s.zmq.SNDBUF > 0 {
			_ = tc.SetWriteBuffer(s.zmq.SNDBUF)
		}
		if s.zmq.RCVBUF > 0 {
			_ = tc.SetReadBuffer(s.zmq.RCVBUF)
		}
		if s.zmq.Linger >= 0 {
			_ = tc.SetLinger(s.zmq.Linger)
		}
	}

	h, r, err := ReadHello(c, 2*time.Second)
	if err != nil {
		v4log.With(map[string]any{"port": port, "status": "hello_error"}).WithError(err).Warn("数据连接被拒绝")
		_ = c.Close()
		return
	}
	bc := &BufferedConn{Conn: c, r: r}

	alloc, ok := s.pool.Allocation(port)
	if !ok || alloc.SessionID != h.SessionID {
		v4log.With(map[string]any{"port": port, "status": "alloc_mismatch", "id": h.ID}).Warn("数据连接被拒绝")
		_ = bc.Close()
		return
	}

	role := h.Role
	s.mu.Lock()
	if _, ok := s.sessions[port]; ok {
		s.mu.Unlock()
		v4log.With(map[string]any{"port": port, "status": "port_occupied", "id": h.ID}).Warn("数据连接冲突")
		_ = bc.Close()
		return
	}
	if role == "server" {
		if s.pendingS[port] != nil {
			s.mu.Unlock()
			v4log.With(map[string]any{"port": port, "status": "server_dup", "id": h.ID}).Warn("数据连接冲突")
			_ = bc.Close()
			return
		}
		s.pendingS[port] = bc
		v4log.With(map[string]any{"port": port, "status": "server_pending", "serverID": h.ID}).Info("数据连接已挂起等待配对")
		s.mu.Unlock()
		go s.tryPair(ctx, port)
		return
	}
	if role == "client" {
		if s.pendingC[port] != nil {
			s.mu.Unlock()
			v4log.With(map[string]any{"port": port, "status": "client_dup", "clientID": h.ID}).Warn("数据连接冲突")
			_ = bc.Close()
			return
		}
		s.pendingC[port] = bc
		v4log.With(map[string]any{"port": port, "status": "client_pending", "clientID": h.ID}).Info("数据连接已挂起等待配对")
		s.mu.Unlock()
		go s.tryPair(ctx, port)
		return
	}
	s.mu.Unlock()
	_ = bc.Close()
}

func (s *tcpServer) pendingMaintenanceLoop(ctx context.Context) {
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

func (s *tcpServer) maintainPending() {
	s.mu.Lock()
	defer s.mu.Unlock()

	for port, c := range s.pendingS {
		alloc, ok := s.pool.Allocation(port)
		if !ok {
			delete(s.pendingS, port)
			_ = c.Close()
			continue
		}
		s.pool.TouchReservation(port, alloc.SessionID)
	}
	for port, c := range s.pendingC {
		alloc, ok := s.pool.Allocation(port)
		if !ok {
			delete(s.pendingC, port)
			_ = c.Close()
			continue
		}
		s.pool.TouchReservation(port, alloc.SessionID)
	}
}

// tryPair 尝试在指定端口把 pending 的 server 与 client 配对成 Session，并推进端口状态为 Occupied。
func (s *tcpServer) tryPair(ctx context.Context, port int) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.sessions[port] != nil {
		return
	}
	sc := s.pendingS[port]
	cc := s.pendingC[port]
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
	sess := NewSession(string(s.kind), port, alloc.SessionID, alloc.ServerID, alloc.ClientID, cc, sc)
	s.sessions[port] = sess

	_ = s.pool.MarkOccupied(port, alloc.SessionID)
	v4log.With(map[string]any{"port": port, "status": "paired", "sessionID": alloc.SessionID}).Info("数据会话开始")

	sess.Start(ctx, func() {
		s.mu.Lock()
		delete(s.sessions, port)
		s.mu.Unlock()
		_ = s.pool.Release(port, alloc.SessionID)
		if s.onSessionStop != nil {
			s.onSessionStop(port, alloc.SessionID)
		}
		v4log.With(map[string]any{"port": port, "status": "session_stop", "sessionID": alloc.SessionID}).Info("数据会话结束")
	})
}

// TickPair 遍历所有监听端口，尝试执行一次配对（用于补偿某些时序下的配对触发）。
func (s *tcpServer) TickPair(ctx context.Context) {
	s.mu.RLock()
	var portsToTry []int
	for port := range s.listeners {
		portsToTry = append(portsToTry, port)
	}
	s.mu.RUnlock()
	for _, port := range portsToTry {
		s.tryPair(ctx, port)
	}
}

// Sessions 返回当前已建立会话列表的快照。
func (s *tcpServer) Sessions() []*Session {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]*Session, 0, len(s.sessions))
	for _, sess := range s.sessions {
		out = append(out, sess)
	}
	return out
}
