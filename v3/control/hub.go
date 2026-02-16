package control

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"relay-x/v1/common"
	"relay-x/v1/config"
	relayv3 "relay-x/v3/relay"
	"sync"
	"sync/atomic"
)

type Client struct {
	Conn    net.Conn
	Role    Role
	Encoder *json.Encoder
	mu      sync.Mutex

	closed    atomic.Bool
	closeOnce sync.Once
}

func (c *Client) Close() {
	c.closeOnce.Do(func() {
		c.closed.Store(true)
		_ = c.Conn.Close()
		GlobalHub.Unregister(c)
	})
}

func (c *Client) Send(msg BaseMessage) error {
	if c.closed.Load() {
		return net.ErrClosed
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.Encoder.Encode(msg)
}

type Hub struct {
	clients map[net.Conn]*Client
	mu      sync.RWMutex
}

var GlobalHub = &Hub{
	clients: make(map[net.Conn]*Client),
}

func (h *Hub) Register(c *Client) {
	h.mu.Lock()
	h.clients[c.Conn] = c
	h.mu.Unlock()
}

func (h *Hub) Unregister(c *Client) {
	h.mu.Lock()
	if _, ok := h.clients[c.Conn]; ok {
		delete(h.clients, c.Conn)
	}
	h.mu.Unlock()
	common.LogInfo("客户端断开连接(V3)", "role", c.Role, "addr", c.Conn.RemoteAddr())
	GlobalPortManager.ReleaseAllForOwner(c)
}

func (h *Hub) Broadcast(msg BaseMessage) {
	h.mu.RLock()
	clients := make([]*Client, 0, len(h.clients))
	for _, c := range h.clients {
		if c != nil && !c.closed.Load() {
			clients = append(clients, c)
		}
	}
	h.mu.RUnlock()

	for _, c := range clients {
		if err := c.Send(msg); err != nil {
			c.Close()
		}
	}
}

func StartServer() error {
	port := config.GlobalConfig.Relay.ControlPort
	if port <= 0 {
		port = 5555
	}
	listener, err := net.Listen("tcp", fmt.Sprintf(":%d", port))
	if err != nil {
		return fmt.Errorf("监听 %d 失败: %w", port, err)
	}
	common.LogInfo("控制平面启动(V3)，监听端口", "port", port)

	for {
		conn, err := listener.Accept()
		if err != nil {
			common.LogError("Accept 失败", "error", err)
			continue
		}
		go handleConnection(conn)
	}
}

func handleConnection(conn net.Conn) {
	client := &Client{
		Conn:    conn,
		Encoder: json.NewEncoder(conn),
	}
	GlobalHub.Register(client)
	defer client.Close()

	decoder := json.NewDecoder(conn)

	_ = client.Send(BaseMessage{Type: MsgAuth, Payload: map[string]string{"status": "ok"}})
	_ = client.Send(BaseMessage{Type: MsgPortAlloc, Payload: PortAllocPayload{
		ZMQStartPort: config.GlobalConfig.Relay.ZMQStartPort,
		ZMQEndPort:   config.GlobalConfig.Relay.ZMQEndPort,
		SRTStartPort: config.GlobalConfig.Relay.SRTStartPort,
		SRTEndPort:   config.GlobalConfig.Relay.SRTEndPort,
	}})
	_ = client.Send(BaseMessage{Type: MsgPortState, Payload: snapshotPortState()})

	for {
		var rawMsg struct {
			Type    MessageType     `json:"type"`
			Payload json.RawMessage `json:"payload"`
		}
		if err := decoder.Decode(&rawMsg); err != nil {
			if errors.Is(err, io.EOF) {
				common.LogInfo("控制连接已关闭(V3)", "role", client.Role, "addr", conn.RemoteAddr())
			} else {
				common.LogError("读取控制消息失败(V3)", "role", client.Role, "addr", conn.RemoteAddr(), "error", err)
			}
			return
		}

		switch rawMsg.Type {
		case MsgRegister:
			var reg RegisterPayload
			if err := json.Unmarshal(rawMsg.Payload, &reg); err == nil {
				client.Role = reg.Role
			}
		case MsgPing:
			var p ClockPayload
			if err := json.Unmarshal(rawMsg.Payload, &p); err == nil {
				HandlePing(client, p)
			}
		case MsgKeyExchange:
			var p KeyExchangePayload
			if err := json.Unmarshal(rawMsg.Payload, &p); err == nil {
				GlobalHub.Broadcast(BaseMessage{Type: MsgKeyExchange, Payload: p})
			}
		case MsgPortAlloc:
			var p PortAllocPayload
			if err := json.Unmarshal(rawMsg.Payload, &p); err == nil {
				GlobalPortManager.SetRanges(p)
				ports := unionPortsFromRanges(p)
				if len(ports) == 0 {
					ports = unionPortsFromConfig()
				}
				for _, port := range ports {
					if port > 0 {
						_ = relayv3.GlobalRelay.StartRelayPort(port)
					}
				}
				GlobalHub.Broadcast(BaseMessage{Type: MsgPortAlloc, Payload: p})
				GlobalHub.Broadcast(BaseMessage{Type: MsgPortState, Payload: snapshotPortState()})
			}
		case MsgPortAcquire:
			var req PortAcquirePayload
			if err := json.Unmarshal(rawMsg.Payload, &req); err == nil {
				grant, err := GlobalPortManager.AcquireFromReady(client, req)
				if err != nil {
					_ = client.Send(BaseMessage{Type: MsgPortGrant, Payload: PortGrantPayload{
						RequestID: req.RequestID,
						ExpiresAt: 0,
						Error:     err.Error(),
					}})
					break
				}
				_ = relayv3.GlobalRelay.StartRelayPort(grant.ZMQPort)
				_ = client.Send(BaseMessage{Type: MsgPortGrant, Payload: grant})
				GlobalHub.Broadcast(BaseMessage{Type: MsgPortGrant, Payload: grant})
			}
		case MsgPortRelease:
			var req PortReleasePayload
			if err := json.Unmarshal(rawMsg.Payload, &req); err == nil {
				ok, port := GlobalPortManager.Release(client, req.SessionID)
				if ok && port > 0 {
					relayv3.GlobalRelay.ResetPort(port)
					GlobalHub.Broadcast(BaseMessage{Type: MsgPortRelease, Payload: req})
					GlobalHub.Broadcast(BaseMessage{Type: MsgPortState, Payload: snapshotPortState()})
				}
			}
		case MsgPortStateQ:
			_ = client.Send(BaseMessage{Type: MsgPortState, Payload: snapshotPortState()})
		default:
		}
	}
}

func snapshotPortState() PortStatePayload {
	snap := relayv3.GlobalRelay.SnapshotPortState()
	return PortStatePayload{
		ZMQTotal:      snap.Total,
		SRTTotal:      snap.Total,
		ZMQBusy:       snap.BusyCount,
		SRTBusy:       snap.BusyCount,
		ZMQReadyPorts: snap.ReadyPorts,
		SRTReadyPorts: snap.ReadyPorts,
		ZMQEmptyPorts: snap.EmptyPorts,
		SRTEmptyPorts: snap.EmptyPorts,
	}
}

func BroadcastPortState() {
	GlobalHub.Broadcast(BaseMessage{Type: MsgPortState, Payload: snapshotPortState()})
}
