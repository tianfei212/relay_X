package config

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

// Load 从 YAML 文件读取并解析配置，并做基础校验与默认值补齐。
// 参数：
// - path: 配置文件路径
// 返回：
// - Config: 合并默认值后的配置
// - error: 读取/解析/校验失败原因
func Load(path string) (Config, error) {
	cfg := DefaultConfig()
	raw, err := os.ReadFile(path)
	if err != nil {
		return Config{}, fmt.Errorf("read config file: %w", err)
	}
	if err := yaml.Unmarshal(raw, &cfg); err != nil {
		return Config{}, fmt.Errorf("unmarshal yaml: %w", err)
	}
	if err := Validate(cfg); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

// Validate 校验配置字段合法性（端口范围、超时、日志输出等）。
// 参数：
// - cfg: 待校验配置
// 返回：
// - error: 校验失败原因
func Validate(cfg Config) error {
	if cfg.Gateway.TCPPort <= 0 || cfg.Gateway.TCPPort > 65535 {
		return fmt.Errorf("invalid gateway.tcp_port: %d", cfg.Gateway.TCPPort)
	}
	if cfg.Gateway.MaxConnections <= 0 {
		return fmt.Errorf("invalid gateway.max_connections: %d", cfg.Gateway.MaxConnections)
	}
	if cfg.Gateway.AuthTimeout <= 0 {
		return fmt.Errorf("invalid gateway.auth_timeout: %s", cfg.Gateway.AuthTimeout)
	}
	if cfg.Gateway.ShutdownTimeout <= 0 {
		return fmt.Errorf("invalid gateway.shutdown_timeout: %s", cfg.Gateway.ShutdownTimeout)
	}
	if _, err := ParsePortRange(cfg.ZeroMQ.PortRange); err != nil {
		return fmt.Errorf("invalid zeromq.port_range: %w", err)
	}
	if _, err := ParsePortRange(cfg.GoSRT.PortRange); err != nil {
		return fmt.Errorf("invalid gosrt.port_range: %w", err)
	}
	if cfg.Logging.Level == "" {
		cfg.Logging.Level = "info"
	}
	if cfg.Logging.Format == "" {
		cfg.Logging.Format = "json"
	}
	if cfg.Logging.Output == "" {
		cfg.Logging.Output = "file"
	}
	if cfg.Logging.Output == "file" && cfg.Logging.FilePath == "" {
		return fmt.Errorf("logging.file_path is required when output=file")
	}
	return nil
}
