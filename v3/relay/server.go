package relay

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"relay-x/v1/common"
	"relay-x/v1/config"
	"sort"
	"sync"
	"time"
)

type RelayServer struct {
	slots        map[int]*portSlot
	onPortFree   func(port int)
	onStateChange func()
	mu           sync.RWMutex
}

type portSlot struct {
	tcpLn  net.Listener
	udpConn *net.UDPConn

	tcpBusy    bool
	tcpSession *PipeSession

	pendingBE   *BufferedConn
	pendingBEID string
	pendingFE   *BufferedConn
	pendingFEID string

	udpA     *net.UDPAddr
	udpB     *net.UDPAddr
	udpLastA time.Time
	udpLastB time.Time
}

type DataHello struct {
	Role     string `json:"role"`
	ClientID string `json:"client_id,omitempty"`
}

const helloPrefix = "RLX1HELLO "

var GlobalRelay = &RelayServer{
	slots: make(map[int]*portSlot),
}

func (s *RelayServer) StartRelayPort(port int) error {
	s.mu.Lock()
	if _, ok := s.slots[port]; ok {
		s.mu.Unlock()
		return nil
	}

	tcpLn, err := net.Listen("tcp", fmt.Sprintf(":%d", port))
	if err != nil {
		s.mu.Unlock()
		return err
	}

	udpConn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4zero, Port: port})
	if err != nil {
		_ = tcpLn.Close()
		s.mu.Unlock()
		return err
	}

	slot := &portSlot{
		tcpLn:   tcpLn,
		udpConn: udpConn,
	}
	s.slots[port] = slot
	s.mu.Unlock()

	common.LogInfo("启动中转端口监听(V3)", "port", port, "tcp", "on", "udp", "on")

	go s.acceptTCPLoop(port, tcpLn)
	go s.udpLoop(port, udpConn)
	go s.udpSweepLoop(port)
	return nil
}

func (s *RelayServer) StopPort(port int) {
	var (
		hook        func(int)
		stateChange func()
		toCloseTCP  net.Listener
		toCloseUDP  *net.UDPConn
		tcpSess     *PipeSession
		pbe         *BufferedConn
		pfe         *BufferedConn
	)

	s.mu.Lock()
	if slot, ok := s.slots[port]; ok && slot != nil {
		toCloseTCP = slot.tcpLn
		toCloseUDP = slot.udpConn
		tcpSess = slot.tcpSession
		pbe = slot.pendingBE
		pfe = slot.pendingFE
		delete(s.slots, port)
	}
	hook = s.onPortFree
	stateChange = s.onStateChange
	s.mu.Unlock()

	if toCloseTCP != nil {
		_ = toCloseTCP.Close()
	}
	if toCloseUDP != nil {
		_ = toCloseUDP.Close()
	}
	if tcpSess != nil {
		tcpSess.Stop()
	}
	if pbe != nil {
		_ = pbe.Close()
	}
	if pfe != nil {
		_ = pfe.Close()
	}

	if hook != nil {
		hook(port)
	}
	if stateChange != nil {
		stateChange()
	}
}

func (s *RelayServer) ResetPort(port int) {
	var stateChange func()
	s.mu.Lock()
	slot := s.slots[port]
	if slot == nil {
		s.mu.Unlock()
		return
	}

	if slot.tcpSession != nil {
		slot.tcpSession.Stop()
		slot.tcpSession = nil
	}
	slot.tcpBusy = false

	if slot.pendingBE != nil {
		_ = slot.pendingBE.Close()
	}
	if slot.pendingFE != nil {
		_ = slot.pendingFE.Close()
	}
	slot.pendingBE = nil
	slot.pendingBEID = ""
	slot.pendingFE = nil
	slot.pendingFEID = ""

	slot.udpA = nil
	slot.udpB = nil
	slot.udpLastA = time.Time{}
	slot.udpLastB = time.Time{}

	stateChange = s.onStateChange
	s.mu.Unlock()
	if stateChange != nil {
		stateChange()
	}
}

func (s *RelayServer) GetSession(port int) *PipeSession {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if slot := s.slots[port]; slot != nil {
		return slot.tcpSession
	}
	return nil
}

func (s *RelayServer) SetOnPortFree(fn func(port int)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.onPortFree = fn
}

func (s *RelayServer) SetOnStateChange(fn func()) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.onStateChange = fn
}

func (s *RelayServer) acceptTCPLoop(port int, l net.Listener) {
	for {
		c, err := l.Accept()
		if err != nil {
			common.LogError("Accept 失败", "port", port, "error", err)
			return
		}

		bc, h := s.wrapConnAndHello(c, 50*time.Millisecond)
		role := helloRole(h)
		id := helloClientID(h)

		var (
			session *PipeSession
			feConn  net.Conn
			beConn  net.Conn
			beID    string
			onStop  func()
			stateChange func()
			freeHook func(int)
		)

		s.mu.Lock()
		slot := s.slots[port]
		if slot == nil {
			s.mu.Unlock()
			_ = bc.Close()
			continue
		}
		if slot.tcpBusy {
			s.mu.Unlock()
			common.LogWarn("TCP端口忙，拒绝连接", "port", port, "addr", c.RemoteAddr())
			_ = bc.Close()
			continue
		}

		if role == "" {
			if slot.pendingBE == nil {
				role = "BE"
			} else if slot.pendingFE == nil {
				role = "FE"
			} else {
				role = "BE"
			}
		}
		s.mu.Unlock()

		common.LogInfo("TCP端口收到连接", "port", port, "addr", c.RemoteAddr(), "role", role, "client_id", id)

		s.mu.Lock()
		slot = s.slots[port]
		if slot == nil {
			s.mu.Unlock()
			_ = bc.Close()
			continue
		}

		switch role {
		case "BE":
			if slot.pendingBE != nil {
				s.mu.Unlock()
				_ = bc.Close()
				continue
			}
			if slot.pendingFE != nil {
				feConn = slot.pendingFE
				beConn = bc
				beID = id
				slot.pendingFE = nil
				slot.pendingFEID = ""
			} else {
				slot.pendingBE = bc
				slot.pendingBEID = id
				stateChange = s.onStateChange
				s.mu.Unlock()
				if stateChange != nil {
					stateChange()
				}
				continue
			}
		case "FE":
			if slot.pendingFE != nil {
				s.mu.Unlock()
				_ = bc.Close()
				continue
			}
			if slot.pendingBE != nil {
				feConn = bc
				beConn = slot.pendingBE
				beID = slot.pendingBEID
				slot.pendingBE = nil
				slot.pendingBEID = ""
			} else {
				slot.pendingFE = bc
				slot.pendingFEID = id
				stateChange = s.onStateChange
				s.mu.Unlock()
				if stateChange != nil {
					stateChange()
				}
				continue
			}
		default:
			s.mu.Unlock()
			_ = bc.Close()
			continue
		}

		onStop = func() {
			s.mu.Lock()
			slot := s.slots[port]
			if slot != nil && slot.tcpSession == session {
				slot.tcpSession = nil
				slot.tcpBusy = false
				if session.KeptBE() {
					if kept, ok := session.ConnBE.(*BufferedConn); ok && slot.pendingBE == nil {
						_ = kept.Conn.SetReadDeadline(time.Time{})
						slot.pendingBE = kept
						slot.pendingBEID = beID
					}
				}
			}
			freeHook = s.onPortFree
			stateChange = s.onStateChange
			s.mu.Unlock()
			if freeHook != nil {
				freeHook(port)
			}
			if stateChange != nil {
				stateChange()
			}
		}

		session = NewPipeSession(feConn, beConn, onStop)
		slot.tcpSession = session
		slot.tcpBusy = true
		stateChange = s.onStateChange
		s.mu.Unlock()

		session.Start()
		if stateChange != nil {
			stateChange()
		}
		common.LogInfo("TCP中转会话启动", "port", port, "fe", feConn.RemoteAddr(), "be", beConn.RemoteAddr())
	}
}

func (s *RelayServer) udpLoop(port int, conn *net.UDPConn) {
	buf := make([]byte, 64*1024)
	for {
		n, addr, err := conn.ReadFromUDP(buf)
		if err != nil {
			common.LogError("UDP Read 失败", "port", port, "error", err)
			return
		}
		if n <= 0 || addr == nil {
			continue
		}

		var (
			dst        *net.UDPAddr
			stateChange func()
		)

		s.mu.Lock()
		slot := s.slots[port]
		if slot == nil {
			s.mu.Unlock()
			continue
		}

		now := time.Now()
		if slot.udpA == nil {
			slot.udpA = cloneUDPAddr(addr)
			slot.udpLastA = now
			stateChange = s.onStateChange
			s.mu.Unlock()
			if stateChange != nil {
				stateChange()
			}
			continue
		}

		if udpAddrEqual(addr, slot.udpA) {
			slot.udpLastA = now
			if slot.udpB != nil {
				dst = slot.udpB
			}
			s.mu.Unlock()
		} else {
			if slot.udpB == nil {
				slot.udpB = cloneUDPAddr(addr)
				slot.udpLastB = now
				stateChange = s.onStateChange
				s.mu.Unlock()
				if stateChange != nil {
					stateChange()
				}
				continue
			}
			if udpAddrEqual(addr, slot.udpB) {
				slot.udpLastB = now
				dst = slot.udpA
				s.mu.Unlock()
			} else {
				s.mu.Unlock()
				continue
			}
		}

		if dst != nil {
			_, _ = conn.WriteToUDP(buf[:n], dst)
		}
	}
}

func (s *RelayServer) udpSweepLoop(port int) {
	timeout := config.GlobalConfig.Relay.ReaperTimeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	for range ticker.C {
		var stateChange func()
		s.mu.Lock()
		slot := s.slots[port]
		if slot == nil {
			s.mu.Unlock()
			return
		}

		now := time.Now()
		if slot.udpA != nil && slot.udpB == nil {
			if !slot.udpLastA.IsZero() && now.Sub(slot.udpLastA) > timeout {
				slot.udpA = nil
				slot.udpLastA = time.Time{}
				stateChange = s.onStateChange
			}
		} else if slot.udpA != nil && slot.udpB != nil {
			aIdle := !slot.udpLastA.IsZero() && now.Sub(slot.udpLastA) > timeout
			bIdle := !slot.udpLastB.IsZero() && now.Sub(slot.udpLastB) > timeout
			if aIdle && bIdle {
				slot.udpA = nil
				slot.udpB = nil
				slot.udpLastA = time.Time{}
				slot.udpLastB = time.Time{}
				stateChange = s.onStateChange
			}
		}
		s.mu.Unlock()
		if stateChange != nil {
			stateChange()
		}
	}
}

type PortStateSnapshot struct {
	Total      int
	BusyCount  int
	ReadyPorts []int
	EmptyPorts []int
}

func (s *RelayServer) SnapshotPortState() PortStateSnapshot {
	s.mu.Lock()
	defer s.mu.Unlock()

	for _, slot := range s.slots {
		if slot == nil {
			continue
		}
		if slot.pendingBE != nil && (slot.pendingBE.Conn == nil) {
			slot.pendingBE = nil
			slot.pendingBEID = ""
		}
		if slot.pendingFE != nil && (slot.pendingFE.Conn == nil) {
			slot.pendingFE = nil
			slot.pendingFEID = ""
		}
		s.cleanupPendingConn(slot, true)
		s.cleanupPendingConn(slot, false)
	}

	var snap PortStateSnapshot
	for port, slot := range s.slots {
		if slot == nil {
			continue
		}
		snap.Total++
		busy := slot.tcpBusy || (slot.udpA != nil && slot.udpB != nil)
		ready := !busy && (slot.pendingBE != nil || slot.pendingFE != nil || slot.udpA != nil || slot.udpB != nil)
		if busy {
			snap.BusyCount++
		} else if ready {
			snap.ReadyPorts = append(snap.ReadyPorts, port)
		} else {
			snap.EmptyPorts = append(snap.EmptyPorts, port)
		}
	}

	sort.Ints(snap.ReadyPorts)
	sort.Ints(snap.EmptyPorts)
	return snap
}

func (s *RelayServer) cleanupPendingConn(slot *portSlot, be bool) {
	var bc *BufferedConn
	if be {
		bc = slot.pendingBE
	} else {
		bc = slot.pendingFE
	}
	if bc == nil || bc.Conn == nil {
		return
	}

	_ = bc.Conn.SetReadDeadline(time.Now())
	_, err := bc.r.Peek(1)
	_ = bc.Conn.SetReadDeadline(time.Time{})
	if err == nil {
		return
	}
	if errors.Is(err, io.EOF) || errors.Is(err, net.ErrClosed) {
		_ = bc.Close()
		if be {
			slot.pendingBE = nil
			slot.pendingBEID = ""
		} else {
			slot.pendingFE = nil
			slot.pendingFEID = ""
		}
	}
}

func (s *RelayServer) wrapConnAndHello(c net.Conn, timeout time.Duration) (*BufferedConn, *DataHello) {
	r := bufio.NewReaderSize(c, 4096)
	bc := &BufferedConn{Conn: c, r: r}

	if timeout > 0 {
		_ = c.SetReadDeadline(time.Now().Add(timeout))
		defer c.SetReadDeadline(time.Time{})
	} else {
		_ = c.SetReadDeadline(time.Now())
		defer c.SetReadDeadline(time.Time{})
	}

	b, err := r.Peek(len(helloPrefix))
	if err != nil || string(b) != helloPrefix {
		return bc, nil
	}
	line, err := r.ReadBytes('\n')
	if err != nil {
		return bc, nil
	}
	if len(line) <= len(helloPrefix) {
		return bc, nil
	}
	raw := line[len(helloPrefix):]
	if raw[len(raw)-1] == '\n' {
		raw = raw[:len(raw)-1]
	}
	var h DataHello
	if err := json.Unmarshal(raw, &h); err != nil {
		return bc, nil
	}
	return bc, &h
}

func helloRole(h *DataHello) string {
	if h == nil {
		return ""
	}
	if h.Role == "FE" || h.Role == "BE" {
		return h.Role
	}
	return ""
}

func helloClientID(h *DataHello) string {
	if h == nil {
		return ""
	}
	return h.ClientID
}

func udpAddrEqual(a, b *net.UDPAddr) bool {
	if a == nil || b == nil {
		return false
	}
	if a.Port != b.Port {
		return false
	}
	if a.Zone != b.Zone {
		return false
	}
	if a.IP == nil && b.IP == nil {
		return true
	}
	if a.IP == nil || b.IP == nil {
		return false
	}
	return a.IP.Equal(b.IP)
}

func cloneUDPAddr(a *net.UDPAddr) *net.UDPAddr {
	if a == nil {
		return nil
	}
	ip := make(net.IP, len(a.IP))
	copy(ip, a.IP)
	return &net.UDPAddr{IP: ip, Port: a.Port, Zone: a.Zone}
}
