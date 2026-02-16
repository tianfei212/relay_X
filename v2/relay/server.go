package relay

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"relay-x/v1/common"
	"sort"
	"sync"
	"time"

	srt "github.com/datarhei/gosrt"
)

type connListener interface {
	AcceptConn() (net.Conn, error)
	Close()
}

type tcpListener struct {
	net.Listener
}

func (l tcpListener) AcceptConn() (net.Conn, error) {
	return l.Accept()
}

func (l tcpListener) Close() {
	_ = l.Listener.Close()
}

type srtListener struct {
	ln srt.Listener
}

func (l srtListener) AcceptConn() (net.Conn, error) {
	req, err := l.ln.Accept2()
	if err != nil {
		return nil, err
	}
	return req.Accept()
}

func (l srtListener) Close() { l.ln.Close() }

type RelayServer struct {
	listeners     map[int]connListener
	sessions      map[int]*PipeSession
	busy          map[int]bool
	pendingBE     map[int]*BufferedConn
	pendingBEID   map[int]string
	pendingFE     map[int]*BufferedConn
	pendingFEID   map[int]string
	portProtocol  map[int]string
	onPortFree    func(port int)
	onStateChange func()
	mu            sync.RWMutex
}

type DataHello struct {
	Role     string `json:"role"`
	ClientID string `json:"client_id,omitempty"`
}

const helloPrefix = "RLX1HELLO "

var GlobalRelay = &RelayServer{
	listeners:    make(map[int]connListener),
	sessions:     make(map[int]*PipeSession),
	busy:         make(map[int]bool),
	pendingBE:    make(map[int]*BufferedConn),
	pendingBEID:  make(map[int]string),
	pendingFE:    make(map[int]*BufferedConn),
	pendingFEID:  make(map[int]string),
	portProtocol: make(map[int]string),
}

func (s *RelayServer) StartRelayPort(port int) error {
	return s.StartRelayPortWithProtocol(port, "")
}

func (s *RelayServer) StartRelayPortWithProtocol(port int, protocol string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, ok := s.listeners[port]; ok {
		return nil
	}

	addr := fmt.Sprintf(":%d", port)
	var l connListener

	if protocol == "srt" {
		addr = fmt.Sprintf("0.0.0.0:%d", port)
		cfg := srt.DefaultConfig()
		ln, lnErr := srt.Listen("srt", addr, cfg)
		if lnErr != nil {
			return lnErr
		}
		l = srtListener{ln: ln}
	} else {
		ln, lnErr := net.Listen("tcp", addr)
		if lnErr != nil {
			return lnErr
		}
		l = tcpListener{Listener: ln}
	}

	s.listeners[port] = l
	if protocol != "" {
		s.portProtocol[port] = protocol
	} else if _, ok := s.portProtocol[port]; !ok {
		s.portProtocol[port] = ""
	}

	if protocol != "" {
		common.LogInfo("启动中转端口监听", "protocol", protocol, "port", port)
	} else {
		common.LogInfo("启动中转端口监听", "port", port)
	}

	go s.acceptLoop(port, l)
	return nil
}

func (s *RelayServer) getPortProtocol(port int) string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.portProtocol[port]
}

func (s *RelayServer) acceptLoop(port int, l connListener) {
	for {
		c, err := l.AcceptConn()
		if err != nil {
			common.LogError("Accept 失败", "port", port, "error", err)
			return
		}
		if s.IsPortBusy(port) {
			common.LogWarn("端口忙，拒绝连接", "port", port, "addr", c.RemoteAddr())
			_ = c.Close()
			continue
		}

		proto := s.getPortProtocol(port)
		readHelloTimeout := 2 * time.Second
		if proto == "zmq" || proto == "srt" {
			readHelloTimeout = 50 * time.Millisecond
		}
		bc, h := s.wrapConnAndHello(c, readHelloTimeout)
		role := helloRole(h)
		id := helloClientID(h)

		var (
			session      *PipeSession
			feConn       net.Conn
			beConn       net.Conn
			beIDForReuse string
			stateChange  func()
		)

		s.mu.Lock()
		if s.busy[port] {
			s.mu.Unlock()
			common.LogWarn("端口忙，拒绝连接", "port", port, "addr", c.RemoteAddr())
			_ = bc.Close()
			continue
		}

		if role == "" {
			if proto == "zmq" || proto == "srt" {
				if s.pendingBE[port] == nil {
					role = "BE"
				} else {
					role = "FE"
				}
			} else {
				s.mu.Unlock()
				common.LogWarn("端口连接缺少合法HELLO，关闭连接", "port", port, "addr", c.RemoteAddr())
				_ = bc.Close()
				continue
			}
		}
		s.mu.Unlock()
		common.LogInfo("端口收到连接", "port", port, "addr", c.RemoteAddr(), "protocol", proto, "role", role, "client_id", id)

		s.mu.Lock()
		switch role {
		case "BE":
			if pending := s.pendingBE[port]; pending != nil {
				s.mu.Unlock()
				common.LogWarn("端口已有BE预连接，拒绝新BE连接", "port", port, "addr", c.RemoteAddr())
				_ = bc.Close()
				continue
			}
			if fePending := s.pendingFE[port]; fePending != nil {
				feConn = fePending
				beConn = bc
				beIDForReuse = id
				delete(s.pendingFE, port)
				delete(s.pendingFEID, port)
			} else {
				s.pendingBE[port] = bc
				s.pendingBEID[port] = id
				stateChange = s.onStateChange
				s.mu.Unlock()
				if stateChange != nil {
					stateChange()
				}
				continue
			}
		case "FE":
			bePending := s.pendingBE[port]
			if bePending == nil {
				s.mu.Unlock()
				common.LogWarn("端口尚无BE预连接，拒绝FE连接", "port", port, "addr", c.RemoteAddr())
				_ = bc.Close()
				continue
			}
			beIDForReuse = s.pendingBEID[port]
			feConn = bc
			beConn = bePending
			delete(s.pendingBE, port)
			delete(s.pendingBEID, port)
		default:
			s.mu.Unlock()
			_ = bc.Close()
			continue
		}

		session = NewPipeSession(feConn, beConn, func() {
			var (
				freeHook    func(int)
				stateChange func()
			)
			s.mu.Lock()
			if cur, ok := s.sessions[port]; ok && cur == session {
				delete(s.sessions, port)
			}
			s.busy[port] = false
			if session.KeptBE() {
				if bc, ok := session.ConnBE.(*BufferedConn); ok && s.pendingBE[port] == nil {
					_ = bc.Conn.SetReadDeadline(time.Time{})
					s.pendingBE[port] = bc
					s.pendingBEID[port] = beIDForReuse
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
		})

		s.sessions[port] = session
		s.busy[port] = true
		stateChange = s.onStateChange
		s.mu.Unlock()

		session.Start()
		if stateChange != nil {
			stateChange()
		}
		common.LogInfo("端口中转会话启动", "port", port, "fe", feConn.RemoteAddr(), "be", beConn.RemoteAddr())
	}
}

func (s *RelayServer) StopPort(port int) {
	var hook func(int)
	var stateChange func()
	s.mu.Lock()
	defer func() {
		s.mu.Unlock()
		if hook != nil {
			hook(port)
		}
		if stateChange != nil {
			stateChange()
		}
	}()

	if l, ok := s.listeners[port]; ok {
		l.Close()
		delete(s.listeners, port)
	}

	if sess, ok := s.sessions[port]; ok {
		sess.Stop()
		delete(s.sessions, port)
	}
	if p := s.pendingBE[port]; p != nil {
		_ = p.Close()
		delete(s.pendingBE, port)
		delete(s.pendingBEID, port)
	}
	if p := s.pendingFE[port]; p != nil {
		_ = p.Close()
		delete(s.pendingFE, port)
		delete(s.pendingFEID, port)
	}
	s.busy[port] = false
	hook = s.onPortFree
	stateChange = s.onStateChange
}

func (s *RelayServer) GetSession(port int) *PipeSession {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.sessions[port]
}

func (s *RelayServer) IsPortBusy(port int) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.busy[port]
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

type PortStateSnapshot struct {
	ZMQReadyPorts []int
	SRTReadyPorts []int
	ZMQEmptyPorts []int
	SRTEmptyPorts []int
	ZMQBusyCount  int
	SRTBusyCount  int
	ZMQTotal      int
	SRTTotal      int
}

func (s *RelayServer) SnapshotPortState() PortStateSnapshot {
	s.mu.Lock()
	defer s.mu.Unlock()

	for port, bc := range s.pendingBE {
		if bc == nil || bc.Conn == nil {
			delete(s.pendingBE, port)
			delete(s.pendingBEID, port)
			continue
		}
		if s.portProtocol[port] == "srt" {
			continue
		}
		_ = bc.Conn.SetReadDeadline(time.Now())
		_, err := bc.r.Peek(1)
		_ = bc.Conn.SetReadDeadline(time.Time{})
		if err == nil {
			continue
		}
		if errors.Is(err, io.EOF) || errors.Is(err, net.ErrClosed) {
			_ = bc.Close()
			delete(s.pendingBE, port)
			delete(s.pendingBEID, port)
			continue
		}
	}

	var snap PortStateSnapshot
	for port := range s.listeners {
		proto := s.portProtocol[port]
		busy := s.busy[port]
		beReady := s.pendingBE[port] != nil
		switch proto {
		case "zmq":
			snap.ZMQTotal++
			if busy {
				snap.ZMQBusyCount++
			} else if beReady {
				snap.ZMQReadyPorts = append(snap.ZMQReadyPorts, port)
			} else {
				snap.ZMQEmptyPorts = append(snap.ZMQEmptyPorts, port)
			}
		case "srt":
			snap.SRTTotal++
			if busy {
				snap.SRTBusyCount++
			} else if beReady {
				snap.SRTReadyPorts = append(snap.SRTReadyPorts, port)
			} else {
				snap.SRTEmptyPorts = append(snap.SRTEmptyPorts, port)
			}
		default:
		}
	}

	sort.Ints(snap.ZMQReadyPorts)
	sort.Ints(snap.SRTReadyPorts)
	sort.Ints(snap.ZMQEmptyPorts)
	sort.Ints(snap.SRTEmptyPorts)
	return snap
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
