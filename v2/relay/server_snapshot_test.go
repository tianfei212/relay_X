package relay

import (
	"bufio"
	"testing"

	"net"
)

func TestSnapshotPortStateCleansClosedPendingBE(t *testing.T) {
	s := &RelayServer{
		listeners:    map[int]connListener{123: nil},
		sessions:     make(map[int]*PipeSession),
		busy:         map[int]bool{123: false},
		pendingBE:    make(map[int]*BufferedConn),
		pendingBEID:  make(map[int]string),
		pendingFE:    make(map[int]*BufferedConn),
		pendingFEID:  make(map[int]string),
		portProtocol: map[int]string{123: "zmq"},
	}

	c1, c2 := net.Pipe()
	_ = c2.Close()
	bc := &BufferedConn{Conn: c1, r: bufio.NewReaderSize(c1, 16)}
	s.pendingBE[123] = bc
	s.pendingBEID[123] = "be-1"

	snap := s.SnapshotPortState()
	if len(snap.ZMQReadyPorts) != 0 {
		t.Fatalf("expected no ready ports, got %v", snap.ZMQReadyPorts)
	}
	if len(snap.ZMQEmptyPorts) != 1 || snap.ZMQEmptyPorts[0] != 123 {
		t.Fatalf("expected port 123 in empty ports, got %v", snap.ZMQEmptyPorts)
	}
	if s.pendingBE[123] != nil {
		t.Fatalf("expected pendingBE to be cleaned")
	}
}
