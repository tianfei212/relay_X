package config

import (
	"os"

	"gopkg.in/yaml.v3"
)

// Version: 1.0
// Developer: GPT-4/JOJO
// Date: 2026-02-15

// Config 定义后端模拟器的配置
type Config struct {
	GatewayAddr  string `yaml:"gateway_addr"`
	DataHost     string `yaml:"data_host"`
	AuthSecret   string `yaml:"auth_secret"`
	BEID         string `yaml:"be_id"`
	ZMQConnLimit int    `yaml:"zmq_conn_limit"`
	SRTConnLimit int    `yaml:"srt_conn_limit"`
	SRTPorts     []int  `yaml:"srt_ports"`
	ZMQPorts     []int  `yaml:"zmq_ports"`
}

// GlobalConfig 全局配置实例
var GlobalConfig *Config

// Load 加载配置文件
func Load(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}

	GlobalConfig = &Config{}
	return yaml.Unmarshal(data, GlobalConfig)
}
