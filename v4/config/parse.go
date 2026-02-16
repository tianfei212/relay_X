package config

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

type PortRange struct {
	Start int
	End   int
}

// ParsePortRange 解析端口范围字符串（形如 "2300-2320"）。
// 参数：
// - s: 端口范围字符串
// 返回：
// - PortRange: 起止端口
// - error: 解析失败原因
func ParsePortRange(s string) (PortRange, error) {
	s = strings.TrimSpace(s)
	parts := strings.Split(s, "-")
	if len(parts) != 2 {
		return PortRange{}, fmt.Errorf("invalid port_range: %q", s)
	}
	start, err := strconv.Atoi(strings.TrimSpace(parts[0]))
	if err != nil {
		return PortRange{}, fmt.Errorf("invalid port_range start: %q", parts[0])
	}
	end, err := strconv.Atoi(strings.TrimSpace(parts[1]))
	if err != nil {
		return PortRange{}, fmt.Errorf("invalid port_range end: %q", parts[1])
	}
	if start <= 0 || end <= 0 || end < start || end > 65535 {
		return PortRange{}, fmt.Errorf("invalid port_range values: %d-%d", start, end)
	}
	return PortRange{Start: start, End: end}, nil
}

type ByteSize int64

// Int64 返回字节数的 int64 表达。
func (b ByteSize) Int64() int64 { return int64(b) }

// UnmarshalYAML 支持从 YAML 中解析 ByteSize（如 100MB、2GB、1024B）。
// 参数：
// - value: YAML 节点
// 返回：
// - error: 解析失败原因
func (b *ByteSize) UnmarshalYAML(value *yaml.Node) error {
	if value == nil {
		*b = 0
		return nil
	}
	v := strings.TrimSpace(value.Value)
	if v == "" {
		*b = 0
		return nil
	}
	n, err := parseByteSize(v)
	if err != nil {
		return err
	}
	*b = ByteSize(n)
	return nil
}

// parseByteSize 解析形如 "100MB"/"1.5GB" 的字节数文本。
// 参数：
// - s: 字节数文本
// 返回：
// - int64: 字节数
// - error: 解析失败原因
func parseByteSize(s string) (int64, error) {
	s = strings.TrimSpace(strings.ToUpper(s))
	mult := int64(1)
	switch {
	case strings.HasSuffix(s, "KB"):
		mult = 1024
		s = strings.TrimSuffix(s, "KB")
	case strings.HasSuffix(s, "MB"):
		mult = 1024 * 1024
		s = strings.TrimSuffix(s, "MB")
	case strings.HasSuffix(s, "GB"):
		mult = 1024 * 1024 * 1024
		s = strings.TrimSuffix(s, "GB")
	case strings.HasSuffix(s, "B"):
		mult = 1
		s = strings.TrimSuffix(s, "B")
	}
	s = strings.TrimSpace(s)
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid byte size: %q", s)
	}
	if f < 0 {
		return 0, fmt.Errorf("invalid byte size: %q", s)
	}
	return int64(f * float64(mult)), nil
}

// DefaultConfig 返回一份可用的默认配置（用于未提供配置文件或作为缺省值合并）。
func DefaultConfig() Config {
	return Config{
		Gateway: GatewayConfig{
			TCPPort:         5555,
			MaxConnections:  10000,
			AuthTimeout:     30 * time.Second,
			ShutdownTimeout: 10 * time.Second,
		},
		ZeroMQ: ZeroMQConfig{
			PortRange:    "2300-2320",
			SNDHWM:       10000,
			RCVHWM:       10000,
			SNDBUF:       2 * 1024 * 1024,
			RCVBUF:       2 * 1024 * 1024,
			Linger:       0,
			ReconnectIVL: 100,
		},
		GoSRT: GoSRTConfig{
			PortRange: "2350-2380",
			Latency:   120,
			MaxBW:     0,
			IPTTL:     64,
		},
		Logging: LoggingConfig{
			Level:    "info",
			Format:   "json",
			Output:   "file",
			FilePath: "/var/log/gateway.log",
			MaxSize:  ByteSize(100 * 1024 * 1024),
			MaxAge:   7,
			Compress: true,
		},
	}
}
