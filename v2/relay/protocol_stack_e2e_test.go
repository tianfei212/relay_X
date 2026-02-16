package relay

import (
	"bytes"
	"fmt"
	"net"
	"testing"
	"time"

	"relay-x/v1/config"
)

func freeTCPPort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen :0: %v", err)
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port
}

func freeUDPPort(t *testing.T) int {
	t.Helper()
	a, err := net.ResolveUDPAddr("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("resolve udp :0: %v", err)
	}
	ln, err := net.ListenUDP("udp", a)
	if err != nil {
		t.Fatalf("listen udp :0: %v", err)
	}
	defer ln.Close()
	return ln.LocalAddr().(*net.UDPAddr).Port
}

func mustDialTCP(t *testing.T, addr string) net.Conn {
	t.Helper()
	c, err := net.DialTimeout("tcp", addr, 2*time.Second)
	if err != nil {
		t.Fatalf("dial tcp %s: %v", addr, err)
	}
	return c
}

func waitSession(t *testing.T, s *RelayServer, port int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if s.GetSession(port) != nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("session not ready port=%d", port)
}

func ensureTestConfig(t *testing.T) {
	t.Helper()
	if config.GlobalConfig.Relay.RingBufferSize <= 0 {
		config.GlobalConfig.Relay.RingBufferSize = 2 * 1024 * 1024
	}
}

func roundTrip(t *testing.T, fe, be net.Conn, a2b, b2a []byte) {
	t.Helper()
	_ = fe.SetDeadline(time.Now().Add(2 * time.Second))
	_ = be.SetDeadline(time.Now().Add(2 * time.Second))

	errCh := make(chan error, 2)

	go func() {
		if _, err := fe.Write(a2b); err != nil {
			errCh <- fmt.Errorf("fe write: %w", err)
			return
		}
		buf := make([]byte, len(a2b))
		if _, err := ioReadFull(be, buf); err != nil {
			errCh <- fmt.Errorf("be read: %w", err)
			return
		}
		if !bytes.Equal(buf, a2b) {
			errCh <- fmt.Errorf("be got mismatch len=%d", len(buf))
			return
		}
		if _, err := be.Write(b2a); err != nil {
			errCh <- fmt.Errorf("be write: %w", err)
			return
		}
		errCh <- nil
	}()

	go func() {
		buf := make([]byte, len(b2a))
		if _, err := ioReadFull(fe, buf); err != nil {
			errCh <- fmt.Errorf("fe read: %w", err)
			return
		}
		if !bytes.Equal(buf, b2a) {
			errCh <- fmt.Errorf("fe got mismatch len=%d", len(buf))
			return
		}
		errCh <- nil
	}()

	for i := 0; i < 2; i++ {
		if err := <-errCh; err != nil {
			t.Fatal(err)
		}
	}
}

func ioReadFull(c net.Conn, dst []byte) (int, error) {
	n := 0
	for n < len(dst) {
		rn, err := c.Read(dst[n:])
		if rn > 0 {
			n += rn
		}
		if err != nil {
			return n, err
		}
	}
	return n, nil
}

func TestProtocolStack_ZMQ_IsTransparentTCPRelayWithoutHello(t *testing.T) {
	ensureTestConfig(t)
	port := freeTCPPort(t)
	addr := fmt.Sprintf("127.0.0.1:%d", port)

	s := &RelayServer{
		listeners:    make(map[int]connListener),
		sessions:     make(map[int]*PipeSession),
		busy:         make(map[int]bool),
		pendingBE:    make(map[int]*BufferedConn),
		pendingBEID:  make(map[int]string),
		pendingFE:    make(map[int]*BufferedConn),
		pendingFEID:  make(map[int]string),
		portProtocol: make(map[int]string),
	}
	if err := s.StartRelayPortWithProtocol(port, "zmq"); err != nil {
		t.Fatalf("StartRelayPortWithProtocol(zmq): %v", err)
	}
	t.Cleanup(func() { s.StopPort(port) })

	be := mustDialTCP(t, addr)
	t.Cleanup(func() { _ = be.Close() })
	fe := mustDialTCP(t, addr)
	t.Cleanup(func() { _ = fe.Close() })

	waitSession(t, s, port)

	a2b := []byte{0xff, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x7f, 0x03, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00}
	b2a := []byte("ZMQ-OK")
	roundTrip(t, fe, be, a2b, b2a)
}

func TestProtocolStack_SRT_UsesSRTListener(t *testing.T) {
	ensureTestConfig(t)
	port := freeUDPPort(t)

	s := &RelayServer{
		listeners:    make(map[int]connListener),
		sessions:     make(map[int]*PipeSession),
		busy:         make(map[int]bool),
		pendingBE:    make(map[int]*BufferedConn),
		pendingBEID:  make(map[int]string),
		pendingFE:    make(map[int]*BufferedConn),
		pendingFEID:  make(map[int]string),
		portProtocol: make(map[int]string),
	}
	if err := s.StartRelayPortWithProtocol(port, "srt"); err != nil {
		t.Fatalf("StartRelayPortWithProtocol(srt): %v", err)
	}
	t.Cleanup(func() { s.StopPort(port) })

	s.mu.RLock()
	l := s.listeners[port]
	s.mu.RUnlock()
	if l == nil {
		t.Fatalf("missing listener port=%d", port)
	}
	if _, ok := l.(srtListener); !ok {
		t.Fatalf("listener is not srtListener: %T", l)
	}
}
