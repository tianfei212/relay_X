package control

import (
	"os"
	"relay-x/v1/common"
	"sync"
)

// Version: 1.0
// Developer: GPT-4/JOJO
// Date: 2026-02-15

// LogStreamer 负责将日志广播给所有连接的客户端
type LogStreamer struct {
	mu sync.Mutex
}

var GlobalLogStreamer = &LogStreamer{}

// Write 实现 io.Writer
func (ls *LogStreamer) Write(p []byte) (n int, err error) {
	// 1. 写入 stdout (保留本地日志)
	n, err = os.Stdout.Write(p)
	
	// 2. 广播
	// 注意：这里的 p 是 []byte，可能是 text 格式的日志行
	// 我们将其作为 Payload 发送
	msg := BaseMessage{
		Type:    MsgLog,
		Payload: string(p),
	}
	
	// 异步广播防止阻塞日志写入
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
