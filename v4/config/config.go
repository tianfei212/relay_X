package config

import "time"

type Config struct {
	Gateway GatewayConfig `yaml:"gateway"`
	ZeroMQ  ZeroMQConfig  `yaml:"zeromq"`
	GoSRT   GoSRTConfig   `yaml:"gosrt"`
	Logging LoggingConfig `yaml:"logging"`
}

type GatewayConfig struct {
	TCPPort         int           `yaml:"tcp_port"`
	MaxConnections  int           `yaml:"max_connections"`
	AuthTimeout     time.Duration `yaml:"auth_timeout"`
	ShutdownTimeout time.Duration `yaml:"shutdown_timeout"`
}

type ZeroMQConfig struct {
	PortRange    string `yaml:"port_range"`
	SNDHWM       int    `yaml:"sndhwm"`
	RCVHWM       int    `yaml:"rcvhwm"`
	SNDBUF       int    `yaml:"sndbuf"`
	RCVBUF       int    `yaml:"rcvbuf"`
	Linger       int    `yaml:"linger"`
	ReconnectIVL int    `yaml:"reconnect_ivl"`
}

type GoSRTConfig struct {
	PortRange string `yaml:"port_range"`
	Latency   int    `yaml:"latency"`
	MaxBW     int64  `yaml:"maxbw"`
	IPTTL     int    `yaml:"ipttl"`
}

type LoggingConfig struct {
	Level    string   `yaml:"level"`
	Format   string   `yaml:"format"`
	Output   string   `yaml:"output"`
	FilePath string   `yaml:"file_path"`
	MaxSize  ByteSize `yaml:"max_size"`
	MaxAge   int      `yaml:"max_age"`
	Compress bool     `yaml:"compress"`
}
