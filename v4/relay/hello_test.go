package relay

import (
	"net"
	"testing"
	"time"
)

// TestReadHelloRejectsMissingPrefix 验证握手前缀缺失会被拒绝。
func TestReadHelloRejectsMissingPrefix(t *testing.T) {
	c1, c2 := net.Pipe()
	defer c1.Close()
	defer c2.Close()

	go func() {
		_, _ = c2.Write([]byte("HELLO {}\n"))
	}()
	_, _, err := ReadHello(c1, 200*time.Millisecond)
	if err == nil {
		t.Fatalf("expected error")
	}
}
