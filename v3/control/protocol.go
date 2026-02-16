package control

type Role string

const (
	RoleFE Role = "FE"
	RoleBE Role = "BE"
)

type MessageType string

const (
	MsgRegister    MessageType = "register"
	MsgAuth        MessageType = "auth"
	MsgPortAlloc   MessageType = "port_alloc"
	MsgPortAcquire MessageType = "port_acquire"
	MsgPortGrant   MessageType = "port_grant"
	MsgPortRelease MessageType = "port_release"
	MsgPortStateQ  MessageType = "port_state_q"
	MsgPortState   MessageType = "port_state"
	MsgKeyExchange MessageType = "key_exchange"
	MsgPing        MessageType = "ping"
	MsgPong        MessageType = "pong"
	MsgTelemetry   MessageType = "telemetry"
	MsgLog         MessageType = "log"
)

type BaseMessage struct {
	Type    MessageType `json:"type"`
	Payload interface{} `json:"payload,omitempty"`
}

type RegisterPayload struct {
	Role     Role   `json:"role"`
	Secret   string `json:"secret,omitempty"`
	ClientID string `json:"client_id,omitempty"`
}

type PortAllocPayload struct {
	ZMQStartPort int `json:"zmq_start_port"`
	ZMQEndPort   int `json:"zmq_end_port"`
	SRTStartPort int `json:"srt_start_port"`
	SRTEndPort   int `json:"srt_end_port"`
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

type KeyExchangePayload struct {
	PublicKey  string `json:"public_key"`
	PrivateKey string `json:"private_key,omitempty"`
}

type ClockPayload struct {
	ClientSendTime int64 `json:"c_send"`
	ServerRecvTime int64 `json:"s_recv"`
	ServerSendTime int64 `json:"s_send"`
}

type TelemetryPayload struct {
	CurrentSpeed float64 `json:"current_speed"`
}
