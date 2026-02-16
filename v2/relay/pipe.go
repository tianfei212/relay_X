package relay

import (
	"errors"
	"io"
	"net"
	"relay-x/v1/common"
	"relay-x/v1/config"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type PipeSession struct {
	ConnFE net.Conn
	ConnBE net.Conn
	addrFE string
	addrBE string
	onStop func()

	bytesIngressFE int64
	bytesEgressFE  int64
	bytesIngressBE int64
	bytesEgressBE  int64

	closeOnce sync.Once
	causeOnce sync.Once
	causeDir  string
	causeOp   string
	causeErr  error
	keepBE    atomic.Bool
	stopping  atomic.Bool

	rbFE2BE *RingBuffer
	rbBE2FE *RingBuffer
}

func NewPipeSession(fe, be net.Conn, onStop func()) *PipeSession {
	var addrFE, addrBE string
	if fe != nil && fe.RemoteAddr() != nil {
		addrFE = fe.RemoteAddr().String()
	}
	if be != nil && be.RemoteAddr() != nil {
		addrBE = be.RemoteAddr().String()
	}

	return &PipeSession{
		ConnFE:  fe,
		ConnBE:  be,
		addrFE:  addrFE,
		addrBE:  addrBE,
		onStop:  onStop,
		rbFE2BE: NewRingBuffer(config.GlobalConfig.Relay.RingBufferSize),
		rbBE2FE: NewRingBuffer(config.GlobalConfig.Relay.RingBufferSize),
	}
}

func (s *PipeSession) Start() {
	common.LogInfo("PipeSession 启动", "fe", s.addrFE, "be", s.addrBE)
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		s.copyLoop("FE->BE", s.ConnBE, s.ConnFE, s.rbFE2BE, &s.bytesIngressFE, &s.bytesEgressBE)
	}()
	go func() {
		defer wg.Done()
		s.copyLoop("BE->FE", s.ConnFE, s.ConnBE, s.rbBE2FE, &s.bytesIngressBE, &s.bytesEgressFE)
	}()
	go func() {
		wg.Wait()
		s.Stop()
		if s.onStop != nil {
			s.onStop()
		}
	}()
}

func (s *PipeSession) Stop() {
	s.closeOnce.Do(func() {
		s.stopping.Store(true)
		s.keepBE.Store(s.shouldKeepBE())

		feIn, feOut, beIn, beOut := s.GetCounters()

		_ = s.ConnFE.SetReadDeadline(time.Now())
		_ = s.ConnBE.SetReadDeadline(time.Now())
		_ = s.ConnFE.Close()
		if !s.keepBE.Load() {
			_ = s.ConnBE.Close()
		}

		s.rbFE2BE.Flush()
		s.rbBE2FE.Flush()
		_ = s.rbFE2BE.CloseWrite()
		_ = s.rbBE2FE.CloseWrite()
		_ = s.rbFE2BE.Close()
		_ = s.rbBE2FE.Close()

		common.LogInfo("PipeSession 停止并清洗缓冲区",
			"fe", s.addrFE,
			"be", s.addrBE,
			"keep_be", s.keepBE.Load(),
			"fe_in", feIn,
			"fe_out", feOut,
			"be_in", beIn,
			"be_out", beOut,
		)
	})
}

func (s *PipeSession) GetCounters() (feIn, feOut, beIn, beOut int64) {
	return atomic.LoadInt64(&s.bytesIngressFE),
		atomic.LoadInt64(&s.bytesEgressFE),
		atomic.LoadInt64(&s.bytesIngressBE),
		atomic.LoadInt64(&s.bytesEgressBE)
}

func (s *PipeSession) copyLoop(dir string, dst io.Writer, src io.Reader, rb *RingBuffer, ingressCounter, egressCounter *int64) {
	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		defer rb.CloseWrite()

		buf := common.GetBuffer()
		defer common.PutBuffer(buf)

		for {
			if c, ok := src.(net.Conn); ok {
				_ = c.SetReadDeadline(time.Now().Add(250 * time.Millisecond))
			}
			n, err := src.Read(buf)
			if n > 0 {
				atomic.AddInt64(ingressCounter, int64(n))
				if _, wErr := rb.Write(buf[:n]); wErr != nil {
					common.LogWarn("PipeSession 写入缓冲区失败", "dir", dir, "error", wErr)
					s.Stop()
					break
				}
			}
			if err != nil {
				if ne, ok := err.(net.Error); ok && ne.Timeout() {
					if s.stopping.Load() {
						break
					}
					continue
				}
				if errors.Is(err, io.EOF) {
					common.LogInfo("PipeSession 读取结束", "dir", dir, "error", err)
				} else {
					common.LogWarn("PipeSession 读取失败", "dir", dir, "error", err)
				}
				s.recordCause(dir, "read", err)
				s.Stop()
				break
			}
			if s.stopping.Load() {
				break
			}
		}
	}()

	go func() {
		defer wg.Done()
		defer rb.Close()

		buf := common.GetBuffer()
		defer common.PutBuffer(buf)

		for {
			n, err := rb.Read(buf)
			if n > 0 {
				if _, wErr := dst.Write(buf[:n]); wErr != nil {
					common.LogWarn("PipeSession 写入目标失败", "dir", dir, "error", wErr)
					s.recordCause(dir, "write", wErr)
					s.Stop()
					break
				}
				atomic.AddInt64(egressCounter, int64(n))
			}
			if err != nil {
				s.Stop()
				if !errors.Is(err, io.EOF) {
					common.LogInfo("PipeSession 缓冲区读取结束", "dir", dir, "error", err)
				}
				break
			}
		}
	}()

	wg.Wait()
}

func (s *PipeSession) recordCause(dir, op string, err error) {
	s.causeOnce.Do(func() {
		s.causeDir = dir
		s.causeOp = op
		s.causeErr = err
	})
}

func (s *PipeSession) shouldKeepBE() bool {
	dir := s.causeDir
	op := s.causeOp
	err := s.causeErr
	if dir == "" || err == nil {
		return false
	}
	if dir == "FE->BE" {
		if op == "read" {
			return isLikelyFEClosed(err)
		}
		return false
	}
	if dir == "BE->FE" {
		if op == "write" {
			return isLikelyFEClosed(err)
		}
		return false
	}
	return false
}

func isLikelyFEClosed(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, io.EOF) {
		return true
	}
	if errors.Is(err, net.ErrClosed) {
		return true
	}
	s := strings.ToLower(err.Error())
	return strings.Contains(s, "broken pipe") ||
		strings.Contains(s, "connection reset by peer") ||
		strings.Contains(s, "use of closed network connection")
}

func (s *PipeSession) KeptBE() bool {
	return s.keepBE.Load()
}
