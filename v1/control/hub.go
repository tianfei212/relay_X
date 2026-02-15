package control

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"relay-x/v1/common"
	"relay-x/v1/config"
	"relay-x/v1/relay"
	"sync"
	"sync/atomic"
	"time"
)

// Version: 1.0
// Developer: GPT-4/JOJO
// Date: 2026-02-15

// Client 代表一个控制连接
type Client struct {
	Conn    net.Conn
	Role    Role
	Encoder *json.Encoder
	mu      sync.Mutex // 保护 Encoder 和 AllocatedPorts

	AllocatedPorts []int

	authed    atomic.Bool
	closed    atomic.Bool
	closeOnce sync.Once
}

// Close 关闭连接
func (c *Client) Close() {
	c.closeOnce.Do(func() {
		c.closed.Store(true)
		_ = c.Conn.Close()
		GlobalHub.Unregister(c)
	})
}

// Send 发送消息
func (c *Client) Send(msg BaseMessage) error {
	if c.closed.Load() {
		return net.ErrClosed
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.Encoder.Encode(msg)
}

// Hub 管理所有控制连接
type Hub struct {
	feClients map[net.Conn]*Client
	beClients map[net.Conn]*Client
	mu        sync.RWMutex
}

var GlobalHub = &Hub{
	feClients: make(map[net.Conn]*Client),
	beClients: make(map[net.Conn]*Client),
}

// Register 注册客户端，返回是否成功
func (h *Hub) Register(c *Client) bool {
	h.mu.Lock()
	defer h.mu.Unlock()

	if c.Role == RoleFE {
		if len(h.feClients) >= config.GlobalConfig.Relay.MaxFE {
			return false
		}
		h.feClients[c.Conn] = c
	} else if c.Role == RoleBE {
		if len(h.beClients) >= config.GlobalConfig.Relay.MaxBE {
			return false
		}
		h.beClients[c.Conn] = c
	} else {
		return false
	}
	return true
}

// Unregister 注销客户端
func (h *Hub) Unregister(c *Client) {
	h.mu.Lock()
	defer h.mu.Unlock()

	// 检查是否在 map 中，防止重复删除
	if c.Role == RoleFE {
		if _, ok := h.feClients[c.Conn]; ok {
			delete(h.feClients, c.Conn)
			common.LogInfo("客户端断开连接", "role", c.Role, "addr", c.Conn.RemoteAddr())
			GlobalPortManager.ReleaseAllForOwner(c)
		}
	} else if c.Role == RoleBE {
		if _, ok := h.beClients[c.Conn]; ok {
			delete(h.beClients, c.Conn)
			common.LogInfo("客户端断开连接", "role", c.Role, "addr", c.Conn.RemoteAddr())

			GlobalPortManager.ReleaseAllForOwner(c)

			// 触发熔断逻辑 (Task 4)
			c.mu.Lock()
			// Copy slice to avoid race if Reaper accesses it (though Reaper takes int slice copy)
			ports := make([]int, len(c.AllocatedPorts))
			copy(ports, c.AllocatedPorts)
			c.mu.Unlock()

			if len(ports) > 0 {
				GlobalReaper.ScheduleKill(ports)
			}
		}
	}
}

// BroadcastToRole 向指定角色广播消息
func (h *Hub) BroadcastToRole(role Role, msg BaseMessage) {
	h.mu.RLock()

	var targets map[net.Conn]*Client
	if role == RoleFE {
		targets = h.feClients
	} else {
		targets = h.beClients
	}

	clients := make([]*Client, 0, len(targets))
	for _, c := range targets {
		if c != nil && c.authed.Load() {
			clients = append(clients, c)
		}
	}
	h.mu.RUnlock()

	for _, client := range clients {
		if err := client.Send(msg); err != nil {
			if !client.closed.Swap(true) {
				common.LogError("广播失败", "role", role, "target", client.Conn.RemoteAddr(), "error", err)
				client.Close()
			}
		}
	}
}

// StartServer 启动 5555 端口监听
func StartServer() error {
	port := config.GlobalConfig.Relay.ControlPort
	if port <= 0 {
		port = 5555
	}
	listener, err := net.Listen("tcp", fmt.Sprintf(":%d", port))
	if err != nil {
		return fmt.Errorf("监听 %d 失败: %v", port, err)
	}
	common.LogInfo("控制平面启动，监听端口", "port", port)

	// 初始化日志流化 (Task 4)
	InitLogStreaming()

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

	decoder := json.NewDecoder(conn)

	// 1. 读取第一条消息 (必须是注册)
	var rawMsg struct {
		Type    MessageType     `json:"type"`
		Payload json.RawMessage `json:"payload"`
	}

	if err := decoder.Decode(&rawMsg); err != nil {
		common.LogError("读取注册消息失败", "addr", conn.RemoteAddr(), "error", err)
		conn.Close()
		return
	}

	if rawMsg.Type != MsgRegister {
		common.LogError("首条消息必须是注册", "addr", conn.RemoteAddr(), "type", rawMsg.Type)
		conn.Close()
		return
	}

	var regPayload RegisterPayload
	if err := json.Unmarshal(rawMsg.Payload, &regPayload); err != nil {
		common.LogError("解析注册载荷失败", "error", err)
		conn.Close()
		return
	}

	// 2. 鉴权
	if !VerifyAuth(regPayload.Secret) {
		common.LogError("鉴权失败: 密钥错误", "addr", conn.RemoteAddr())
		conn.Close()
		return
	}

	client.Role = regPayload.Role

	// 3. 注册并检查连接数
	if !GlobalHub.Register(client) {
		common.LogError("连接数超限，拒绝连接", "role", client.Role)
		conn.Close()
		return
	}

	// 发送 Auth 成功消息
	if err := client.Send(BaseMessage{
		Type:    MsgAuth,
		Payload: map[string]string{"status": "ok"},
	}); err != nil {
		conn.Close()
		return
	}
	client.authed.Store(true)

	common.LogInfo("客户端注册成功", "role", client.Role, "addr", conn.RemoteAddr())

	if client.Role == RoleFE {
		p := PortAllocPayload{
			ZMQStartPort: config.GlobalConfig.Relay.ZMQStartPort,
			ZMQEndPort:   config.GlobalConfig.Relay.ZMQEndPort,
			SRTStartPort: config.GlobalConfig.Relay.SRTStartPort,
			SRTEndPort:   config.GlobalConfig.Relay.SRTEndPort,
		}
		_ = client.Send(BaseMessage{Type: MsgPortAlloc, Payload: p})
		snap := relay.GlobalRelay.SnapshotPortState()
		_ = client.Send(BaseMessage{Type: MsgPortState, Payload: PortStatePayload{
			ZMQTotal:      snap.ZMQTotal,
			SRTTotal:      snap.SRTTotal,
			ZMQBusy:       snap.ZMQBusyCount,
			SRTBusy:       snap.SRTBusyCount,
			ZMQReadyPorts: snap.ZMQReadyPorts,
			SRTReadyPorts: snap.SRTReadyPorts,
			ZMQEmptyPorts: snap.ZMQEmptyPorts,
			SRTEmptyPorts: snap.SRTEmptyPorts,
		}})
	} else if client.Role == RoleBE {
		snap := relay.GlobalRelay.SnapshotPortState()
		_ = client.Send(BaseMessage{Type: MsgPortState, Payload: PortStatePayload{
			ZMQTotal:      snap.ZMQTotal,
			SRTTotal:      snap.SRTTotal,
			ZMQBusy:       snap.ZMQBusyCount,
			SRTBusy:       snap.SRTBusyCount,
			ZMQReadyPorts: snap.ZMQReadyPorts,
			SRTReadyPorts: snap.SRTReadyPorts,
			ZMQEmptyPorts: snap.ZMQEmptyPorts,
			SRTEmptyPorts: snap.SRTEmptyPorts,
		}})
	}

	// 4. 保持连接并处理后续消息
	defer client.Close()

	for {
		var nextRawMsg struct {
			Type    MessageType     `json:"type"`
			Payload json.RawMessage `json:"payload"`
		}

		if err := decoder.Decode(&nextRawMsg); err != nil {
			if errors.Is(err, io.EOF) {
				common.LogInfo("控制连接已关闭", "role", client.Role, "addr", conn.RemoteAddr())
			} else {
				common.LogError("读取控制消息失败", "role", client.Role, "addr", conn.RemoteAddr(), "error", err)
			}
			return
		}

		switch nextRawMsg.Type {
		case MsgPing:
			var clockPayload ClockPayload
			if err := json.Unmarshal(nextRawMsg.Payload, &clockPayload); err == nil {
				HandlePing(client, clockPayload)
			}
		case MsgKeyExchange:
			var keyPayload KeyExchangePayload
			if err := json.Unmarshal(nextRawMsg.Payload, &keyPayload); err == nil {
				if client.Role == RoleBE {
					GlobalHub.BroadcastToRole(RoleFE, BaseMessage{
						Type:    MsgKeyExchange,
						Payload: keyPayload,
					})
					common.LogInfo("密钥透传完成: BE -> FE")
				}
			}
		case MsgPortAlloc:
			var portPayload PortAllocPayload
			if err := json.Unmarshal(nextRawMsg.Payload, &portPayload); err == nil {
				if client.Role == RoleBE {
					cfgPayload := PortAllocPayload{
						ZMQStartPort: config.GlobalConfig.Relay.ZMQStartPort,
						ZMQEndPort:   config.GlobalConfig.Relay.ZMQEndPort,
						SRTStartPort: config.GlobalConfig.Relay.SRTStartPort,
						SRTEndPort:   config.GlobalConfig.Relay.SRTEndPort,
					}
					if cfgPayload.ZMQStartPort != 0 || cfgPayload.ZMQEndPort != 0 || cfgPayload.SRTStartPort != 0 || cfgPayload.SRTEndPort != 0 {
						portPayload = cfgPayload
					}
					GlobalPortManager.SetRanges(portPayload)
					var zmqPorts []int
					if portPayload.ZMQStartPort <= portPayload.ZMQEndPort {
						zmqPorts = make([]int, 0, portPayload.ZMQEndPort-portPayload.ZMQStartPort+1)
						for p := portPayload.ZMQStartPort; p <= portPayload.ZMQEndPort; p++ {
							zmqPorts = append(zmqPorts, p)
						}
					}

					var srtPorts []int
					if portPayload.SRTStartPort <= portPayload.SRTEndPort {
						srtPorts = make([]int, 0, portPayload.SRTEndPort-portPayload.SRTStartPort+1)
						for p := portPayload.SRTStartPort; p <= portPayload.SRTEndPort; p++ {
							srtPorts = append(srtPorts, p)
						}
					}
					allPorts := make([]int, 0, len(zmqPorts)+len(srtPorts))
					allPorts = append(allPorts, zmqPorts...)
					allPorts = append(allPorts, srtPorts...)

					common.LogInfo("收到端口分配请求",
						"addr", conn.RemoteAddr(),
						"zmq_start", portPayload.ZMQStartPort,
						"zmq_end", portPayload.ZMQEndPort,
						"srt_start", portPayload.SRTStartPort,
						"srt_end", portPayload.SRTEndPort,
					)

					for _, p := range zmqPorts {
						if err := relay.GlobalRelay.StartRelayPortWithProtocol(p, "zmq"); err != nil {
							common.LogError("启动中转端口失败", "protocol", "zmq", "port", p, "error", err)
						}
					}
					for _, p := range srtPorts {
						if err := relay.GlobalRelay.StartRelayPortWithProtocol(p, "srt"); err != nil {
							common.LogError("启动中转端口失败", "protocol", "srt", "port", p, "error", err)
						}
					}

					client.mu.Lock()
					client.AllocatedPorts = allPorts
					portsToCancel := make([]int, len(allPorts))
					copy(portsToCancel, allPorts)
					client.mu.Unlock()

					// 取消之前的熔断 (如果是重连)
					GlobalReaper.CancelKill(portsToCancel)

					// 启动遥测
					go startTelemetry(client)

					GlobalHub.BroadcastToRole(RoleFE, BaseMessage{
						Type:    MsgPortAlloc,
						Payload: portPayload,
					})
					common.LogInfo("端口分配广播完成: BE -> FE", "ports", len(portsToCancel))
				}
			}
		case MsgPortAcquire:
			var req PortAcquirePayload
			if err := json.Unmarshal(nextRawMsg.Payload, &req); err == nil {
				if client.Role == RoleBE || client.Role == RoleFE {
					var (
						grant PortGrantPayload
						err   error
					)
					if client.Role == RoleFE {
						grant, err = GlobalPortManager.AcquireFromReady(client, req)
					} else {
						grant, err = GlobalPortManager.Acquire(client, req)
					}
					if err != nil {
						_ = client.Send(BaseMessage{
							Type: MsgPortGrant,
							Payload: PortGrantPayload{
								RequestID: req.RequestID,
								ExpiresAt: time.Now().UnixMilli(),
								Error:     err.Error(),
							},
						})
						break
					}

					_ = client.Send(BaseMessage{Type: MsgPortGrant, Payload: grant})
					if client.Role == RoleBE {
						GlobalHub.BroadcastToRole(RoleFE, BaseMessage{Type: MsgPortGrant, Payload: grant})
					} else {
						GlobalHub.BroadcastToRole(RoleBE, BaseMessage{Type: MsgPortGrant, Payload: grant})
					}
				}
			}
		case MsgPortRelease:
			var req PortReleasePayload
			if err := json.Unmarshal(nextRawMsg.Payload, &req); err == nil {
				if client.Role == RoleBE || client.Role == RoleFE {
					if GlobalPortManager.Release(client, req.SessionID) {
						if client.Role == RoleBE {
							GlobalHub.BroadcastToRole(RoleFE, BaseMessage{Type: MsgPortRelease, Payload: req})
						} else {
							GlobalHub.BroadcastToRole(RoleBE, BaseMessage{Type: MsgPortRelease, Payload: req})
						}
					}
				}
			}
		case MsgPortStateQ:
			snap := relay.GlobalRelay.SnapshotPortState()
			_ = client.Send(BaseMessage{Type: MsgPortState, Payload: PortStatePayload{
				ZMQTotal:      snap.ZMQTotal,
				SRTTotal:      snap.SRTTotal,
				ZMQBusy:       snap.ZMQBusyCount,
				SRTBusy:       snap.SRTBusyCount,
				ZMQReadyPorts: snap.ZMQReadyPorts,
				SRTReadyPorts: snap.SRTReadyPorts,
				ZMQEmptyPorts: snap.ZMQEmptyPorts,
				SRTEmptyPorts: snap.SRTEmptyPorts,
			}})
		default:
			// 忽略未知消息或仅记录
		}
	}
}

func startTelemetry(c *Client) {
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()

	var lastTotal int64

	for {
		select {
		case <-ticker.C:
			// 检查连接状态 (通过 c.Send 隐式检查)

			var currentTotal int64
			c.mu.Lock()
			// Make a copy to avoid holding lock during loop
			ports := make([]int, len(c.AllocatedPorts))
			copy(ports, c.AllocatedPorts)
			c.mu.Unlock()

			for _, p := range ports {
				sess := relay.GlobalRelay.GetSession(p)
				if sess != nil {
					fi, fo, bi, bo := sess.GetCounters()
					currentTotal += fi + fo + bi + bo
				}
			}

			delta := currentTotal - lastTotal
			if delta < 0 {
				delta = 0
			}
			lastTotal = currentTotal

			// Mbps = (bytes * 8) / (0.1s * 1e6) = bytes * 80 / 1e6
			speedMbps := float64(delta) * 80.0 / 1000000.0

			msg := BaseMessage{
				Type: MsgTelemetry,
				Payload: TelemetryPayload{
					CurrentSpeed: speedMbps,
				},
			}

			if err := c.Send(msg); err != nil {
				return
			}
		}
	}
}
