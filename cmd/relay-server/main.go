package main

import (
	"log/slog"
	"relay-x/v1/common"
	"relay-x/v1/config"
	"relay-x/v1/control"
	"relay-x/v1/relay"
)

// Version: 1.0
// Developer: GPT-4/JOJO
// Date: 2026-02-15

func main() {
	// 1. 初始化日志
	common.InitLogger()
	common.LogInfo("正在初始化 JOJO-Relay-X 网关...")

	// 2. 加载配置
	if err := config.Load(); err != nil {
		common.LogError("配置加载失败", "error", err)
		return
	}

	if err := common.SetLogLevel(config.GlobalConfig.Log.Level); err != nil {
		common.LogWarn("日志等级配置无效，使用默认值", "level", config.GlobalConfig.Log.Level, "error", err)
	} else {
		common.LogInfo("日志等级已设置", slog.String("level", config.GlobalConfig.Log.Level))
	}

	common.LogInfo("配置加载成功",
		slog.Int("MaxFE", config.GlobalConfig.Relay.MaxFE),
		slog.Int("MaxBE", config.GlobalConfig.Relay.MaxBE),
		slog.Int64("RingBufferSize", config.GlobalConfig.Relay.RingBufferSize),
		slog.Duration("ReaperTimeout", config.GlobalConfig.Relay.ReaperTimeout),
	)

	p := control.PortAllocPayload{
		ZMQStartPort: config.GlobalConfig.Relay.ZMQStartPort,
		ZMQEndPort:   config.GlobalConfig.Relay.ZMQEndPort,
		SRTStartPort: config.GlobalConfig.Relay.SRTStartPort,
		SRTEndPort:   config.GlobalConfig.Relay.SRTEndPort,
	}
	common.LogInfo("数据面端口段已加载",
		slog.Int("zmq_start", p.ZMQStartPort),
		slog.Int("zmq_end", p.ZMQEndPort),
		slog.Int("srt_start", p.SRTStartPort),
		slog.Int("srt_end", p.SRTEndPort),
	)
	control.GlobalPortManager.SetRanges(p)
	for port := p.ZMQStartPort; port <= p.ZMQEndPort; port++ {
		if port > 0 {
			if err := relay.GlobalRelay.StartRelayPortWithProtocol(port, "zmq"); err != nil {
				common.LogWarn("启动中转端口监听失败", "protocol", "zmq", "port", port, "error", err)
			}
		}
	}
	for port := p.SRTStartPort; port <= p.SRTEndPort; port++ {
		if port > 0 {
			if err := relay.GlobalRelay.StartRelayPortWithProtocol(port, "srt"); err != nil {
				common.LogWarn("启动中转端口监听失败", "protocol", "srt", "port", port, "error", err)
			}
		}
	}

	relay.GlobalRelay.SetOnStateChange(func() {
		snap := relay.GlobalRelay.SnapshotPortState()
		msg := control.BaseMessage{
			Type: control.MsgPortState,
			Payload: control.PortStatePayload{
				ZMQTotal:      snap.ZMQTotal,
				SRTTotal:      snap.SRTTotal,
				ZMQBusy:       snap.ZMQBusyCount,
				SRTBusy:       snap.SRTBusyCount,
				ZMQReadyPorts: snap.ZMQReadyPorts,
				SRTReadyPorts: snap.SRTReadyPorts,
				ZMQEmptyPorts: snap.ZMQEmptyPorts,
				SRTEmptyPorts: snap.SRTEmptyPorts,
			},
		}
		control.GlobalHub.BroadcastToRole(control.RoleBE, msg)
		control.GlobalHub.BroadcastToRole(control.RoleFE, msg)
	})

	// 3. 启动控制平面
	go func() {
		if err := control.StartServer(); err != nil {
			common.LogError("控制平面启动失败", "error", err)
		}
	}()

	// 阻塞主线程
	select {}
}
