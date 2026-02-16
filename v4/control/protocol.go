package control

type Role string

const (
	RoleServer Role = "server"
	RoleClient Role = "client"
)

type MessageType string

const (
	MsgRegister     MessageType = "register"
	MsgRegisterAck  MessageType = "register_ack"
	MsgPairRequest  MessageType = "pair_request"
	MsgPairGrant    MessageType = "pair_grant"
	MsgPairError    MessageType = "pair_error"
	MsgStatus       MessageType = "status"
	MsgTelemetry    MessageType = "telemetry"
	MsgError        MessageType = "error"
	MsgPortState    MessageType = "port_state"
	MsgPortConflict MessageType = "port_conflict"
)

type Envelope struct {
	Type    MessageType `json:"type"`
	Payload any         `json:"payload,omitempty"`
}

type RegisterPayload struct {
	Role    Role   `json:"role"`
	ID      string `json:"id"`
	Token   string `json:"token"`
	MaxConn int    `json:"max_conn"`
}

type RegisterAckPayload struct {
	Status string `json:"status"`
	Code   int    `json:"code,omitempty"`
	Error  string `json:"error,omitempty"`
}

type PairRequestPayload struct {
	RequestID string `json:"request_id"`
	ServerID  string `json:"server_id,omitempty"`
	NeedZMQ   bool   `json:"need_zmq"`
	NeedSRT   bool   `json:"need_srt"`
	QoS       any    `json:"qos,omitempty"`
}

type PairGrantPayload struct {
	RequestID   string `json:"request_id"`
	SessionID   string `json:"session_id"`
	ServerID    string `json:"server_id"`
	ClientID    string `json:"client_id"`
	ServerToken string `json:"server_token,omitempty"`
	ClientToken string `json:"client_token,omitempty"`
	ZMQPort     int    `json:"zmq_port,omitempty"`
	SRTPort     int    `json:"srt_port,omitempty"`
}

type ErrorPayload struct {
	Code  int    `json:"code"`
	Error string `json:"error"`
}

type ServerStatusPayload struct {
	ServerID      string  `json:"server_id"`
	OccupiedPorts []int   `json:"occupied_ports"`
	Remaining     int     `json:"remaining_ports"`
	CPUPercent    float64 `json:"cpu_percent"`
	MemMB         float64 `json:"mem_mb"`
}

type TelemetryPayload struct {
	Kind      string  `json:"kind"`
	Port      int     `json:"port"`
	SessionID string  `json:"session_id"`
	ServerID  string  `json:"server_id"`
	ClientID  string  `json:"client_id"`
	MbpsUp    float64 `json:"mbps_up"`
	MbpsDown  float64 `json:"mbps_down"`
	RTTMs     float64 `json:"rtt_ms"`
	LossPct   float64 `json:"loss_pct"`
	RetrPct   float64 `json:"retrans_pct"`
	Quality   float64 `json:"quality"`
}

type PortStatePayload struct {
	ZMQ PortStateKindPayload `json:"zmq"`
	SRT PortStateKindPayload `json:"srt"`
}

type PortStateKindPayload struct {
	Total     int   `json:"total"`
	Idle      int   `json:"idle"`
	Reserved  int   `json:"reserved"`
	Occupied  int   `json:"occupied"`
	Blocked   int   `json:"blocked"`
	IdlePorts []int `json:"idle_ports,omitempty"`
}

type HealthStatusPayload struct {
	Status          string           `json:"status"`
	StartedAtUnixMs int64            `json:"started_at_unix_ms"`
	NowUnixMs       int64            `json:"now_unix_ms"`
	Connections     int              `json:"connections"`
	BroadcastUnixMs int64            `json:"broadcast_unix_ms"`
	PortState       PortStatePayload `json:"port_state"`
}
