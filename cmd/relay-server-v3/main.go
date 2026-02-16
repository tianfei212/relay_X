package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"relay-x/v1/common"
	"relay-x/v1/config"
	"relay-x/v3/control"
	relayv3 "relay-x/v3/relay"
	"strings"

	"gopkg.in/yaml.v3"
)

func main() {
	common.InitLogger()
	common.LogInfo("正在初始化 JOJO-Relay-X 网关(V3)...")

	configArg := flag.String("config", "", "配置路径：目录(含 config.yaml 与 .env.local.json)或 YAML 文件路径")
	flag.Parse()

	yamlPath, envPath, err := resolveConfigPaths(strings.TrimSpace(*configArg))
	if err != nil {
		common.LogError("配置参数解析失败", "error", err)
		return
	}
	if err := loadFromFiles(yamlPath, envPath); err != nil {
		common.LogError("配置加载失败", "error", err)
		return
	}

	if err := common.SetLogLevel(config.GlobalConfig.Log.Level); err != nil {
		common.LogWarn("日志等级配置无效，使用默认值", "level", config.GlobalConfig.Log.Level, "error", err)
	} else {
		common.LogInfo("日志等级已设置", slog.String("level", config.GlobalConfig.Log.Level))
	}

	control.InitPortManager()
	control.GlobalPortManager.SetRanges(control.PortAllocPayload{
		ZMQStartPort: config.GlobalConfig.Relay.ZMQStartPort,
		ZMQEndPort:   config.GlobalConfig.Relay.ZMQEndPort,
		SRTStartPort: config.GlobalConfig.Relay.SRTStartPort,
		SRTEndPort:   config.GlobalConfig.Relay.SRTEndPort,
	})

	ports := []int{}
	if config.GlobalConfig.Relay.ZMQStartPort > 0 && config.GlobalConfig.Relay.ZMQEndPort >= config.GlobalConfig.Relay.ZMQStartPort {
		for p := config.GlobalConfig.Relay.ZMQStartPort; p <= config.GlobalConfig.Relay.ZMQEndPort; p++ {
			ports = append(ports, p)
		}
	}
	if config.GlobalConfig.Relay.SRTStartPort > 0 && config.GlobalConfig.Relay.SRTEndPort >= config.GlobalConfig.Relay.SRTStartPort {
		for p := config.GlobalConfig.Relay.SRTStartPort; p <= config.GlobalConfig.Relay.SRTEndPort; p++ {
			ports = append(ports, p)
		}
	}

	for _, p := range ports {
		if err := relayv3.GlobalRelay.StartRelayPort(p); err != nil {
			common.LogWarn("启动中转端口监听失败", "port", p, "error", err)
		}
	}

	relayv3.GlobalRelay.SetOnStateChange(func() {
		control.BroadcastPortState()
	})

	go func() {
		if err := control.StartServer(); err != nil {
			common.LogError("控制平面启动失败", "error", err)
		}
	}()

	select {}
}

func loadFromFiles(yamlPath, envPath string) error {
	yamlFile, err := os.ReadFile(yamlPath)
	if err != nil {
		return fmt.Errorf("读取 config.yaml 失败: %w", err)
	}
	if err := yaml.Unmarshal(yamlFile, &config.GlobalConfig); err != nil {
		return fmt.Errorf("解析 config.yaml 失败: %w", err)
	}

	jsonFile, err := os.ReadFile(envPath)
	if err != nil {
		return fmt.Errorf("读取 .env.local.json 失败: %w", err)
	}
	if err := json.Unmarshal(jsonFile, &config.GlobalConfig.Env); err != nil {
		return fmt.Errorf("解析 .env.local.json 失败: %w", err)
	}
	return nil
}

func resolveConfigPaths(configArg string) (string, string, error) {
	defaultYaml := "configs/config.yaml"
	defaultEnv := ".env.local.json"
	if configArg == "" {
		return defaultYaml, defaultEnv, nil
	}

	fi, err := os.Stat(configArg)
	if err != nil {
		return "", "", fmt.Errorf("配置路径不存在 path=%s: %w", configArg, err)
	}
	if fi.IsDir() {
		yamlPath := filepath.Join(configArg, "config.yaml")
		envPath := filepath.Join(configArg, ".env.local.json")
		if _, err := os.Stat(yamlPath); err != nil {
			return "", "", fmt.Errorf("目录下缺少 config.yaml: %w", err)
		}
		if _, err := os.Stat(envPath); err != nil {
			return "", "", fmt.Errorf("目录下缺少 .env.local.json: %w", err)
		}
		return yamlPath, envPath, nil
	}

	if strings.HasSuffix(strings.ToLower(configArg), ".yaml") || strings.HasSuffix(strings.ToLower(configArg), ".yml") {
		dir := filepath.Dir(configArg)
		envPath := filepath.Join(dir, ".env.local.json")
		if _, err := os.Stat(envPath); err != nil {
			return "", "", fmt.Errorf("YAML 同目录下缺少 .env.local.json: %w", err)
		}
		return configArg, envPath, nil
	}

	return "", "", fmt.Errorf("不支持的配置参数：必须是目录或 .yaml/.yml 文件 path=%s", configArg)
}
