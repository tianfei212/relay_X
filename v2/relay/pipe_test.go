package relay

import (
	"net"
	"relay-x/v1/config"
	"testing"
	"time"
)

func TestPipeSessionStopsWhenOneSideCloses(t *testing.T) {
	config.GlobalConfig.Relay.RingBufferSize = 1024 * 1024

	fe1, fe2 := net.Pipe()
	be1, be2 := net.Pipe()
	defer fe2.Close()
	defer be2.Close()

	done := make(chan struct{})
	s := NewPipeSession(fe1, be1, func() { close(done) })
	s.Start()

	_ = fe2.Close()

	select {
	case <-done:
	case <-time.After(1 * time.Second):
		t.Fatalf("PipeSession未在断连后停止")
	}
}
