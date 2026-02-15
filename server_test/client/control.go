package client

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net"
	"server_test/config"
	"strings"
	"sync"
	"time"
)

// Version: 1.0
// Developer: GPT-4/JOJO
// Date: 2026-02-15

// ControlClient 5555 控制面客户端
type ControlClient struct {
	conn    net.Conn
	encoder *json.Encoder
	decoder *json.Decoder
	mu      sync.Mutex

	lastPortAlloc   PortAllocPayload
	hasPortAlloc    bool
	lastKeyExchange KeyExchangePayload
	hasKeyExchange  bool
	portGrantCh     chan PortGrantPayload
	portStateCh     chan PortStatePayload

	lastPingSendNs int64
	lastPingRTTMs  float64
}

// NewControlClient 创建新的控制面客户端
func NewControlClient() *ControlClient {
	return &ControlClient{
		portGrantCh: make(chan PortGrantPayload, 256),
		portStateCh: make(chan PortStatePayload, 64),
	}
}

// Connect 连接到网关
func (c *ControlClient) Connect() error {
	conn, err := net.Dial("tcp", config.GlobalConfig.GatewayAddr)
	if err != nil {
		return fmt.Errorf("连接网关失败: %v", err)
	}

	c.conn = conn
	c.encoder = json.NewEncoder(conn)
	c.decoder = json.NewDecoder(conn)

	fmt.Printf("[控制-5555] 已连接到网关: %s\n", config.GlobalConfig.GatewayAddr)
	return nil
}

// Register 发送注册消息
func (c *ControlClient) Register() error {
	regMsg := BaseMessage{
		Type: MsgRegister,
		Payload: RegisterPayload{
			Role:   RoleBE,
			Secret: config.GlobalConfig.AuthSecret,
		},
	}

	if err := c.encoder.Encode(regMsg); err != nil {
		return fmt.Errorf("发送注册消息失败: %v", err)
	}

	fmt.Println("[控制-5555] 已发送注册消息 (BE)")
	return nil
}

// WaitForAuth 等待认证响应
func (c *ControlClient) WaitForAuth() error {
	mt, payload, err := c.readRaw()
	if err != nil {
		return fmt.Errorf("接收认证消息失败: %v", err)
	}
	if mt != MsgAuth {
		return fmt.Errorf("认证失败，收到消息类型: %s", mt)
	}

	var auth struct {
		Status string `json:"status"`
	}
	_ = json.Unmarshal(payload, &auth)
	if auth.Status != "" && auth.Status != "ok" {
		return fmt.Errorf("认证失败，status=%s", auth.Status)
	}

	fmt.Println("[控制-5555] 认证成功")
	return nil
}

// SendPortAllocation 发送端口分配请求
func (c *ControlClient) SendPortAllocation() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if len(config.GlobalConfig.ZMQPorts) == 0 || len(config.GlobalConfig.SRTPorts) == 0 {
		return fmt.Errorf("配置端口为空")
	}

	payload := PortAllocPayload{
		ZMQStartPort: config.GlobalConfig.ZMQPorts[0],
		ZMQEndPort:   config.GlobalConfig.ZMQPorts[len(config.GlobalConfig.ZMQPorts)-1],
		SRTStartPort: config.GlobalConfig.SRTPorts[0],
		SRTEndPort:   config.GlobalConfig.SRTPorts[len(config.GlobalConfig.SRTPorts)-1],
	}

	portAllocMsg := BaseMessage{
		Type:    MsgPortAlloc,
		Payload: payload,
	}

	if err := c.encoder.Encode(portAllocMsg); err != nil {
		return fmt.Errorf("发送端口分配消息失败: %v", err)
	}

	c.lastPortAlloc = payload
	c.hasPortAlloc = true

	fmt.Printf("[控制-5555] 已发送端口分配: ZMQ=[%d-%d] SRT=[%d-%d]\n",
		payload.ZMQStartPort, payload.ZMQEndPort, payload.SRTStartPort, payload.SRTEndPort)
	return nil
}

// SendKeyExchange 发送密钥交换消息
func (c *ControlClient) SendKeyExchange() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	payload := KeyExchangePayload{
		PublicKey:  "dummy_public_key",
		PrivateKey: "temp_private_key_" + config.GlobalConfig.BEID,
	}

	keyExchangeMsg := BaseMessage{
		Type:    MsgKeyExchange,
		Payload: payload,
	}

	if err := c.encoder.Encode(keyExchangeMsg); err != nil {
		return fmt.Errorf("发送密钥交换消息失败: %v", err)
	}

	c.lastKeyExchange = payload
	c.hasKeyExchange = true

	fmt.Println("[控制-5555] 已发送密钥交换消息")
	return nil
}

func (c *ControlClient) AcquirePort(req PortAcquirePayload, timeout time.Duration) (PortGrantPayload, error) {
	c.mu.Lock()
	err := c.encoder.Encode(BaseMessage{
		Type:    MsgPortAcquire,
		Payload: req,
	})
	c.mu.Unlock()
	if err != nil {
		return PortGrantPayload{}, fmt.Errorf("发送端口租用失败: %v", err)
	}

	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	_ = c.conn.SetReadDeadline(time.Now().Add(timeout))
	defer c.conn.SetReadDeadline(time.Time{})

	for {
		mt, payload, err := c.readRaw()
		if err != nil {
			return PortGrantPayload{}, fmt.Errorf("等待端口租用结果失败: %v", err)
		}
		if mt != MsgPortGrant {
			continue
		}
		var g PortGrantPayload
		if err := json.Unmarshal(payload, &g); err != nil {
			continue
		}
		if req.RequestID != "" && g.RequestID != req.RequestID {
			continue
		}
		if g.Error != "" {
			return g, fmt.Errorf("端口租用失败: %s", g.Error)
		}
		return g, nil
	}
}

func (c *ControlClient) StartPingLoop(interval time.Duration, stop <-chan struct{}) {
	t := time.NewTicker(interval)
	defer t.Stop()

	for {
		select {
		case <-stop:
			return
		case <-t.C:
			c.mu.Lock()
			sendNs := time.Now().UnixNano()
			c.lastPingSendNs = sendNs
			msg := BaseMessage{
				Type: MsgPing,
				Payload: ClockPayload{
					ClientSendTime: sendNs,
					ServerRecvTime: 0,
					ServerSendTime: 0,
				},
			}
			err := c.encoder.Encode(msg)
			c.mu.Unlock()
			if err != nil {
				fmt.Printf("[控制-5555] 发送Ping失败: %v\n", err)
				return
			}
		}
	}
}

// StartListening 启动消息监听循环
func (c *ControlClient) StartListening() {
	for {
		mt, payload, err := c.readRaw()
		if err != nil {
			fmt.Printf("[控制-5555] 接收消息失败: %v\n", err)
			return
		}

		switch mt {
		case MsgLog:
			logLine := c.decodeLog(payload)
			if logLine != "" {
				fmt.Printf("[控制-5555][LOG] %s\n", strings.TrimRight(logLine, "\n"))
				if strings.Contains(logLine, "客户端注册成功") && strings.Contains(logLine, "role=FE") {
					c.resendForNewFE()
				}
			}
		case MsgTelemetry:
			var t TelemetryPayload
			if err := json.Unmarshal(payload, &t); err == nil {
				if t.CurrentSpeed <= 0 {
					break
				}
				fmt.Printf("[控制-5555][TELEMETRY] current_speed=%.2f Mbps\n", t.CurrentSpeed)
			} else {
				break
			}
		case MsgPong:
			var clock ClockPayload
			if err := json.Unmarshal(payload, &clock); err == nil && clock.ClientSendTime > 0 {
				now := time.Now().UnixNano()
				rttMs := float64(now-clock.ClientSendTime) / 1e6
				jitterMs := rttMs - c.lastPingRTTMs
				if jitterMs < 0 {
					jitterMs = -jitterMs
				}
				c.lastPingRTTMs = rttMs
				fmt.Printf("[控制-5555][PING] rtt=%.2fms jitter=%.2fms\n", rttMs, jitterMs)
			}
		case MsgPortGrant:
			var g PortGrantPayload
			if err := json.Unmarshal(payload, &g); err == nil {
				select {
				case c.portGrantCh <- g:
				default:
				}
			}
		case MsgPortState:
			var s PortStatePayload
			if err := json.Unmarshal(payload, &s); err == nil {
				select {
				case c.portStateCh <- s:
				default:
				}
			}
		default:
			fmt.Printf("[控制-5555] 收到消息 type=%s payload=%s\n", mt, string(payload))
		}
	}
}

func (c *ControlClient) PortGrantChan() <-chan PortGrantPayload {
	return c.portGrantCh
}

func (c *ControlClient) PortStateChan() <-chan PortStatePayload {
	return c.portStateCh
}

// Close 关闭连接
func (c *ControlClient) Close() {
	if c.conn != nil {
		c.conn.Close()
	}
}

func (c *ControlClient) resendForNewFE() {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.hasPortAlloc {
		_ = c.encoder.Encode(BaseMessage{Type: MsgPortAlloc, Payload: c.lastPortAlloc})
		fmt.Printf("[控制-5555] 检测到FE上线，重发端口分配: ZMQ=[%d-%d] SRT=[%d-%d]\n",
			c.lastPortAlloc.ZMQStartPort, c.lastPortAlloc.ZMQEndPort, c.lastPortAlloc.SRTStartPort, c.lastPortAlloc.SRTEndPort)
	}
	if c.hasKeyExchange {
		_ = c.encoder.Encode(BaseMessage{Type: MsgKeyExchange, Payload: c.lastKeyExchange})
		fmt.Println("[控制-5555] 检测到FE上线，重发密钥交换")
	}
}

func (c *ControlClient) decodeLog(payload []byte) string {
	if len(payload) == 0 {
		return ""
	}
	if payload[0] == '"' {
		var s string
		if err := json.Unmarshal(payload, &s); err == nil {
			return s
		}
	}

	var obj map[string]any
	if err := json.Unmarshal(payload, &obj); err == nil {
		b, _ := json.Marshal(obj)
		return string(b)
	}

	return string(bytes.TrimSpace(payload))
}

func (c *ControlClient) readRaw() (MessageType, []byte, error) {
	var raw struct {
		Type    MessageType     `json:"type"`
		Payload json.RawMessage `json:"payload"`
	}
	if err := c.decoder.Decode(&raw); err != nil {
		return "", nil, err
	}
	return raw.Type, raw.Payload, nil
}
