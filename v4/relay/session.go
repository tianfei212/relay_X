package relay

import (
	"context"
	"errors"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"time"

	v4log "relay-x/v4/log"
)

type Session struct {
	kind      string
	port      int
	sessionID string
	serverID  string
	clientID  string

	fe net.Conn
	be net.Conn

	counters counters

	mu         sync.RWMutex
	lastReadFE time.Time
	lastReadBE time.Time

	closeOnce sync.Once
	done      chan struct{}
}

// NewSession 创建一条前后端之间的转发会话。
// 参数：
// - kind: 会话类型标识（如 "zmq"/"srt"）
// - port: 数据端口
// - sessionID/serverID/clientID: 控制面下发的绑定信息
// - fe: 前端连接（client）
// - be: 后端连接（server）
func NewSession(kind string, port int, sessionID, serverID, clientID string, fe, be net.Conn) *Session {
	now := time.Now()
	return &Session{
		kind:       kind,
		port:       port,
		sessionID:  sessionID,
		serverID:   serverID,
		clientID:   clientID,
		fe:         fe,
		be:         be,
		lastReadFE: now,
		lastReadBE: now,
		done:       make(chan struct{}),
	}
}

// Start 启动会话的双向转发（FE->BE 与 BE->FE）。
// 参数：
// - ctx: 用于统一取消的上下文
// - onStop: 会话结束时的回调（可为 nil）
func (s *Session) Start(ctx context.Context, onStop func()) {
	go func() {
		defer func() {
			s.Stop()
			if onStop != nil {
				onStop()
			}
		}()

		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			s.pipe(ctx, "FE->BE", s.be, s.fe, &s.counters.feIn, &s.counters.beOut, true)
		}()
		go func() {
			defer wg.Done()
			s.pipe(ctx, "BE->FE", s.fe, s.be, &s.counters.beIn, &s.counters.feOut, false)
		}()

		wg.Wait()
	}()
}

// pipe 执行单向数据拷贝，并累计字节计数与心跳时间。
// 参数：
// - ctx: 上下文
// - dir: 方向标签（仅用于日志字段）
// - dst/src: 目标写入与来源读取
// - inCounter/outCounter: 字节计数器
// - isFERead: 是否为从 FE 读取（用于更新 FE 心跳）
func (s *Session) pipe(ctx context.Context, dir string, dst io.Writer, src io.Reader, inCounter, outCounter *atomic.Int64, isFERead bool) {
	buf := getBuf()
	defer putBuf(buf)

	for {
		select {
		case <-ctx.Done():
			return
		case <-s.done:
			return
		default:
		}

		if c, ok := src.(net.Conn); ok {
			_ = c.SetReadDeadline(time.Now().Add(250 * time.Millisecond))
		}

		n, err := src.Read(buf)
		if n > 0 {
			inCounter.Add(int64(n))
			if isFERead {
				s.touchFE()
			} else {
				s.touchBE()
			}
			wn, werr := dst.Write(buf[:n])
			if wn > 0 {
				outCounter.Add(int64(wn))
			}
			if werr != nil {
				v4log.With(map[string]any{
					"port":      s.port,
					"sessionID": s.sessionID,
					"dir":       dir,
					"status":    "write_error",
				}).WithError(werr).Warn("数据转发写入失败")
				s.Stop()
				return
			}
		}
		if err != nil {
			if ne, ok := err.(net.Error); ok && ne.Timeout() {
				continue
			}
			if errors.Is(err, io.EOF) || errors.Is(err, net.ErrClosed) {
				s.Stop()
				return
			}
			v4log.With(map[string]any{
				"port":      s.port,
				"sessionID": s.sessionID,
				"dir":       dir,
				"status":    "read_error",
			}).WithError(err).Warn("数据转发读取失败")
			s.Stop()
			return
		}
	}
}

// Stop 关闭会话并释放两端连接（幂等）。
func (s *Session) Stop() {
	s.closeOnce.Do(func() {
		close(s.done)
		if s.fe != nil {
			_ = s.fe.SetDeadline(time.Now())
			_ = s.fe.Close()
		}
		if s.be != nil {
			_ = s.be.SetDeadline(time.Now())
			_ = s.be.Close()
		}
	})
}

// touchFE 更新前端读活跃时间（用于心跳超时检测）。
func (s *Session) touchFE() {
	s.mu.Lock()
	s.lastReadFE = time.Now()
	s.mu.Unlock()
}

// touchBE 更新后端读活跃时间（用于心跳超时检测）。
func (s *Session) touchBE() {
	s.mu.Lock()
	s.lastReadBE = time.Now()
	s.mu.Unlock()
}

// LastReadAges 返回距离最近一次读到数据的时长（前端/后端各一份）。
func (s *Session) LastReadAges() (feAge, beAge time.Duration) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	now := time.Now()
	return now.Sub(s.lastReadFE), now.Sub(s.lastReadBE)
}

// Counters 返回累计字节计数（前端入/前端出/后端入/后端出）。
func (s *Session) Counters() (feIn, feOut, beIn, beOut int64) {
	return s.counters.feIn.Load(), s.counters.feOut.Load(), s.counters.beIn.Load(), s.counters.beOut.Load()
}
