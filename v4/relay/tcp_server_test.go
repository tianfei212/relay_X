package relay

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"testing"
	"time"

	"relay-x/v4/config"
	"relay-x/v4/ports"
)

// TestTCPPortUniqueness 验证同一 TCP 数据端口严格一对一（server+client），重复连接会被拒绝。
func TestTCPPortUniqueness(t *testing.T) {
	port := freeTCPPort(t)
	pool, err := ports.NewPool(ports.KindZMQ, port, port)
	if err != nil {
		t.Fatal(err)
	}
	_, err = pool.Reserve("sess1", "srv1", "cli1")
	if err != nil {
		t.Fatal(err)
	}

	srv := newTCPServer(ports.KindZMQ, pool, config.ZeroMQConfig{SNDBUF: 1 << 20, RCVBUF: 1 << 20, Linger: 0})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := srv.StartPort(ctx, port); err != nil {
		t.Fatal(err)
	}
	defer srv.StopAll()

	s1 := dialAndHello(t, port, "server", "sess1", "srv1")
	defer s1.Close()

	s2 := dialAndHello(t, port, "server", "sess1", "srv2")
	defer s2.Close()
	expectClosedSoon(t, s2)

	c1 := dialAndHello(t, port, "client", "sess1", "cli1")
	defer c1.Close()

	waitFor(t, 2*time.Second, func() bool {
		return pool.Snapshot().Occupied == 1
	})

	c2 := dialAndHello(t, port, "client", "sess1", "cli2")
	defer c2.Close()
	expectClosedSoon(t, c2)

	_ = c1.Close()
	_ = s1.Close()
	waitFor(t, 5*time.Second, func() bool {
		return pool.Snapshot().Idle == 1
	})
}

// freeTCPPort 获取一个可用的临时 TCP 端口（用于测试）。
func freeTCPPort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port
}

// dialAndHello 建立 TCP 连接并发送 V4HELLO 握手行。
func dialAndHello(t *testing.T, port int, role, sessionID, id string) net.Conn {
	t.Helper()
	c, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	line := fmt.Sprintf("%s{\"role\":\"%s\",\"session_id\":\"%s\",\"id\":\"%s\"}\n", HelloPrefix, role, sessionID, id)
	if _, err := c.Write([]byte(line)); err != nil {
		_ = c.Close()
		t.Fatal(err)
	}
	return c
}

// expectClosedSoon 断言连接在短时间内变为不可读（通常是被服务端关闭）。
func expectClosedSoon(t *testing.T, c net.Conn) {
	t.Helper()
	_ = c.SetReadDeadline(time.Now().Add(300 * time.Millisecond))
	r := bufio.NewReader(c)
	_, err := r.ReadByte()
	if err == nil {
		t.Fatalf("expected closed")
	}
}

// waitFor 在超时时间内轮询等待条件成立。
func waitFor(t *testing.T, d time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timeout")
}
