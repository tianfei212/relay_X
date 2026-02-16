package status

import (
	"encoding/json"
	"fmt"
	"strings"
)

type GatewayStatus string

const (
	GatewayStarting GatewayStatus = "Starting"
	GatewayRunning  GatewayStatus = "Running"
	GatewayStopping GatewayStatus = "Stopping"
	GatewayStopped  GatewayStatus = "Stopped"
)

// String 返回网关状态文本。
func (s GatewayStatus) String() string { return string(s) }

// ParseGatewayStatus 将文本解析为 GatewayStatus。
// 参数：
// - v: 状态文本（Starting/Running/Stopping/Stopped）
// 返回：
// - GatewayStatus: 解析结果
// - error: 未知状态时返回错误
func ParseGatewayStatus(v string) (GatewayStatus, error) {
	switch strings.TrimSpace(v) {
	case string(GatewayStarting):
		return GatewayStarting, nil
	case string(GatewayRunning):
		return GatewayRunning, nil
	case string(GatewayStopping):
		return GatewayStopping, nil
	case string(GatewayStopped):
		return GatewayStopped, nil
	default:
		return "", fmt.Errorf("unknown GatewayStatus: %q", v)
	}
}

// MarshalJSON 将 GatewayStatus 编码为 JSON 字符串。
func (s GatewayStatus) MarshalJSON() ([]byte, error) { return json.Marshal(string(s)) }

// UnmarshalJSON 从 JSON 字符串解码为 GatewayStatus。
func (s *GatewayStatus) UnmarshalJSON(b []byte) error {
	var v string
	if err := json.Unmarshal(b, &v); err != nil {
		return err
	}
	parsed, err := ParseGatewayStatus(v)
	if err != nil {
		return err
	}
	*s = parsed
	return nil
}

type PortStatus string

const (
	PortIdle     PortStatus = "Idle"
	PortOccupied PortStatus = "Occupied"
	PortReserved PortStatus = "Reserved"
	PortBlocked  PortStatus = "Blocked"
)

// String 返回端口状态文本。
func (s PortStatus) String() string { return string(s) }

// ParsePortStatus 将文本解析为 PortStatus。
// 参数：
// - v: 状态文本（Idle/Occupied/Reserved/Blocked）
// 返回：
// - PortStatus: 解析结果
// - error: 未知状态时返回错误
func ParsePortStatus(v string) (PortStatus, error) {
	switch strings.TrimSpace(v) {
	case string(PortIdle):
		return PortIdle, nil
	case string(PortOccupied):
		return PortOccupied, nil
	case string(PortReserved):
		return PortReserved, nil
	case string(PortBlocked):
		return PortBlocked, nil
	default:
		return "", fmt.Errorf("unknown PortStatus: %q", v)
	}
}

// MarshalJSON 将 PortStatus 编码为 JSON 字符串。
func (s PortStatus) MarshalJSON() ([]byte, error) { return json.Marshal(string(s)) }

// UnmarshalJSON 从 JSON 字符串解码为 PortStatus。
func (s *PortStatus) UnmarshalJSON(b []byte) error {
	var v string
	if err := json.Unmarshal(b, &v); err != nil {
		return err
	}
	parsed, err := ParsePortStatus(v)
	if err != nil {
		return err
	}
	*s = parsed
	return nil
}

type ConnStatus string

const (
	ConnInit         ConnStatus = "Init"
	ConnHandshake    ConnStatus = "Handshake"
	ConnEstablished  ConnStatus = "Established"
	ConnDisconnected ConnStatus = "Disconnected"
	ConnError        ConnStatus = "Error"
)

// String 返回连接状态文本。
func (s ConnStatus) String() string { return string(s) }

// ParseConnStatus 将文本解析为 ConnStatus。
// 参数：
// - v: 状态文本（Init/Handshake/Established/Disconnected/Error）
// 返回：
// - ConnStatus: 解析结果
// - error: 未知状态时返回错误
func ParseConnStatus(v string) (ConnStatus, error) {
	switch strings.TrimSpace(v) {
	case string(ConnInit):
		return ConnInit, nil
	case string(ConnHandshake):
		return ConnHandshake, nil
	case string(ConnEstablished):
		return ConnEstablished, nil
	case string(ConnDisconnected):
		return ConnDisconnected, nil
	case string(ConnError):
		return ConnError, nil
	default:
		return "", fmt.Errorf("unknown ConnStatus: %q", v)
	}
}

// MarshalJSON 将 ConnStatus 编码为 JSON 字符串。
func (s ConnStatus) MarshalJSON() ([]byte, error) { return json.Marshal(string(s)) }

// UnmarshalJSON 从 JSON 字符串解码为 ConnStatus。
func (s *ConnStatus) UnmarshalJSON(b []byte) error {
	var v string
	if err := json.Unmarshal(b, &v); err != nil {
		return err
	}
	parsed, err := ParseConnStatus(v)
	if err != nil {
		return err
	}
	*s = parsed
	return nil
}

type AuthStatus string

const (
	AuthPending  AuthStatus = "Pending"
	AuthApproved AuthStatus = "Approved"
	AuthRejected AuthStatus = "Rejected"
	AuthExpired  AuthStatus = "Expired"
)

// String 返回鉴权状态文本。
func (s AuthStatus) String() string { return string(s) }

// ParseAuthStatus 将文本解析为 AuthStatus。
// 参数：
// - v: 状态文本（Pending/Approved/Rejected/Expired）
// 返回：
// - AuthStatus: 解析结果
// - error: 未知状态时返回错误
func ParseAuthStatus(v string) (AuthStatus, error) {
	switch strings.TrimSpace(v) {
	case string(AuthPending):
		return AuthPending, nil
	case string(AuthApproved):
		return AuthApproved, nil
	case string(AuthRejected):
		return AuthRejected, nil
	case string(AuthExpired):
		return AuthExpired, nil
	default:
		return "", fmt.Errorf("unknown AuthStatus: %q", v)
	}
}

// MarshalJSON 将 AuthStatus 编码为 JSON 字符串。
func (s AuthStatus) MarshalJSON() ([]byte, error) { return json.Marshal(string(s)) }

// UnmarshalJSON 从 JSON 字符串解码为 AuthStatus。
func (s *AuthStatus) UnmarshalJSON(b []byte) error {
	var v string
	if err := json.Unmarshal(b, &v); err != nil {
		return err
	}
	parsed, err := ParseAuthStatus(v)
	if err != nil {
		return err
	}
	*s = parsed
	return nil
}

type FlowDirection string

const (
	FlowUnknown   FlowDirection = "Unknown"
	FlowSending   FlowDirection = "Sending"
	FlowReceiving FlowDirection = "Receiving"
	FlowDuplex    FlowDirection = "Duplex"
)

// String 返回流向文本。
func (s FlowDirection) String() string { return string(s) }

// ParseFlowDirection 将文本解析为 FlowDirection。
// 参数：
// - v: 流向文本（Unknown/Sending/Receiving/Duplex）
// 返回：
// - FlowDirection: 解析结果
// - error: 未知流向时返回错误
func ParseFlowDirection(v string) (FlowDirection, error) {
	switch strings.TrimSpace(v) {
	case string(FlowUnknown):
		return FlowUnknown, nil
	case string(FlowSending):
		return FlowSending, nil
	case string(FlowReceiving):
		return FlowReceiving, nil
	case string(FlowDuplex):
		return FlowDuplex, nil
	default:
		return "", fmt.Errorf("unknown FlowDirection: %q", v)
	}
}

// MarshalJSON 将 FlowDirection 编码为 JSON 字符串。
func (s FlowDirection) MarshalJSON() ([]byte, error) { return json.Marshal(string(s)) }

// UnmarshalJSON 从 JSON 字符串解码为 FlowDirection。
func (s *FlowDirection) UnmarshalJSON(b []byte) error {
	var v string
	if err := json.Unmarshal(b, &v); err != nil {
		return err
	}
	parsed, err := ParseFlowDirection(v)
	if err != nil {
		return err
	}
	*s = parsed
	return nil
}
