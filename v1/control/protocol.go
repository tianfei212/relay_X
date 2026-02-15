package control

// Version: 1.0
// Developer: GPT-4/JOJO
// Date: 2026-02-15

// Role 定义客户端角色
type Role string

const (
	RoleFE Role = "FE"
	RoleBE Role = "BE"
)

// MessageType 定义消息类型
type MessageType string

const (
	MsgRegister    MessageType = "register"     // 注册
	MsgAuth        MessageType = "auth"         // 鉴权结果
	MsgPortAlloc   MessageType = "port_alloc"   // 端口分配
	MsgPortAcquire MessageType = "port_acquire" // 端口租用
	MsgPortGrant   MessageType = "port_grant"   // 端口租用结果
	MsgPortRelease MessageType = "port_release" // 端口释放
	MsgPortStateQ  MessageType = "port_state_q" // 端口状态查询
	MsgPortState   MessageType = "port_state"   // 端口状态响应/推送
	MsgKeyExchange MessageType = "key_exchange" // 密钥交换
	MsgPing        MessageType = "ping"         // 心跳/对时
	MsgPong        MessageType = "pong"         // 心跳/对时响应
	MsgTelemetry   MessageType = "telemetry"    // 遥测数据
	MsgLog         MessageType = "log"          // 日志推送
)

// BaseMessage 基础消息结构
type BaseMessage struct {
	Type    MessageType `json:"type"`
	Payload interface{} `json:"payload,omitempty"`
}

// RegisterPayload 注册载荷
type RegisterPayload struct {
	Role     Role   `json:"role"`
	Secret   string `json:"secret,omitempty"` // 鉴权密钥
	ClientID string `json:"client_id,omitempty"`
}

// PortAllocPayload 端口分配载荷
type PortAllocPayload struct {
	ZMQStartPort int `json:"zmq_start_port"` // 2302
	ZMQEndPort   int `json:"zmq_end_port"`   // 2310
	SRTStartPort int `json:"srt_start_port"` // 2320
	SRTEndPort   int `json:"srt_end_port"`   // 2330
}

type PortAcquirePayload struct {
	RequestID  string `json:"request_id,omitempty"`
	NeedZMQ    bool   `json:"need_zmq"`
	NeedSRT    bool   `json:"need_srt"`
	TTLSeconds int    `json:"ttl_seconds,omitempty"`
}

type PortGrantPayload struct {
	RequestID string `json:"request_id,omitempty"`
	SessionID string `json:"session_id"`
	ZMQPort   int    `json:"zmq_port,omitempty"`
	SRTPort   int    `json:"srt_port,omitempty"`
	ExpiresAt int64  `json:"expires_at"`
	Error     string `json:"error,omitempty"`
}

type PortReleasePayload struct {
	SessionID string `json:"session_id"`
}

type PortStateQueryPayload struct {
	NeedZMQ bool `json:"need_zmq,omitempty"`
	NeedSRT bool `json:"need_srt,omitempty"`
}

type PortStatePayload struct {
	ZMQTotal int `json:"zmq_total"`
	SRTTotal int `json:"srt_total"`

	ZMQBusy int `json:"zmq_busy"`
	SRTBusy int `json:"srt_busy"`

	ZMQReadyPorts []int `json:"zmq_ready_ports"`
	SRTReadyPorts []int `json:"srt_ready_ports"`

	ZMQEmptyPorts []int `json:"zmq_empty_ports"`
	SRTEmptyPorts []int `json:"srt_empty_ports"`
}

// KeyExchangePayload 密钥交换载荷
type KeyExchangePayload struct {
	PublicKey  string `json:"public_key"`
	PrivateKey string `json:"private_key,omitempty"` // 仅 BE 发送给网关时可能包含
}

// ClockPayload 时钟同步载荷
type ClockPayload struct {
	ClientSendTime int64 `json:"c_send"` // 客户端发送时间 (ns)
	ServerRecvTime int64 `json:"s_recv"` // 服务端接收时间 (ns)
	ServerSendTime int64 `json:"s_send"` // 服务端发送时间 (ns)
}

// TelemetryPayload 遥测载荷
type TelemetryPayload struct {
	CurrentSpeed float64 `json:"current_speed"` // Mbps
}
