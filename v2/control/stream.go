package control

import (
	"os"
	"relay-x/v1/common"
	"sync"
)

// LogStreamer 负责将日志广播给所有连接的客户端
type LogStreamer struct {
	mu sync.Mutex
}

var GlobalLogStreamer = &LogStreamer{}

// Write 实现 io.Writer
func (ls *LogStreamer) Write(p []byte) (n int, err error) {
	n, err = os.Stdout.Write(p)

	msg := BaseMessage{
		Type:    MsgLog,
		Payload: string(p),
	}

	go func(m BaseMessage) {
		GlobalHub.BroadcastToRole(RoleFE, m)
		GlobalHub.BroadcastToRole(RoleBE, m)
	}(msg)

	return n, err
}

// InitLogStreaming 初始化日志流化
func InitLogStreaming() {
	common.SetOutput(GlobalLogStreamer)
	common.LogInfo("日志流化已开启")
}
