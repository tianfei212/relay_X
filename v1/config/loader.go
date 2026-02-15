package config

import (
	"encoding/json"
	"fmt"
	"os"
	"time"

	"gopkg.in/yaml.v3"
)

// Version: 1.0
// Developer: GPT-4/JOJO
// Date: 2026-02-15

// RelayConfig 包含所有中转服务的静态配置
type RelayConfig struct {
	MaxFE          int           `yaml:"max_fe"`           // 最大前端并发
	MaxBE          int           `yaml:"max_be"`           // 最大后端并发
	RingBufferSize int64         `yaml:"ring_buffer_size"` // 单路缓冲区大小 (bytes)
	ReaperTimeout  time.Duration `yaml:"reaper_timeout"`   // 断连熔断阈值
	ControlPort    int           `yaml:"control_port"`
	ZMQStartPort   int           `yaml:"zmq_start_port"`
	ZMQEndPort     int           `yaml:"zmq_end_port"`
	SRTStartPort   int           `yaml:"srt_start_port"`
	SRTEndPort     int           `yaml:"srt_end_port"`
}

// EnvConfig 包含敏感信息
type EnvConfig struct {
	RelayPublicKey string `json:"relay_public_key"` // 杭州网关公钥
	AuthSecret     string `json:"auth_secret"`      // 后端校验密钥
}

type LogConfig struct {
	Level string `yaml:"level"`
}

// Config 聚合所有配置
type Config struct {
	Relay RelayConfig `yaml:"relay"`
	Log   LogConfig   `yaml:"log"`
	Env   EnvConfig
}

var GlobalConfig Config

// Load 加载并解析所有配置文件
// 功能说明:
// 1. 读取 configs/config.yaml 解析静态配置
// 2. 读取 .env.local.json 解析敏感信息
// 3. 校验关键参数是否合法
func Load() error {
	// 1. 读取 YAML
	yamlFile, err := os.ReadFile("configs/config.yaml")
	if err != nil {
		return fmt.Errorf("读取 config.yaml 失败: %v", err)
	}
	if err := yaml.Unmarshal(yamlFile, &GlobalConfig); err != nil {
		return fmt.Errorf("解析 config.yaml 失败: %v", err)
	}

	// 2. 读取 JSON
	jsonFile, err := os.ReadFile(".env.local.json")
	if err != nil {
		return fmt.Errorf("读取 .env.local.json 失败: %v", err)
	}
	if err := json.Unmarshal(jsonFile, &GlobalConfig.Env); err != nil {
		return fmt.Errorf("解析 .env.local.json 失败: %v", err)
	}

	return nil
}
