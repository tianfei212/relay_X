package control

import (
	"fmt"
	"relay-x/v1/common"
	"relay-x/v1/config"
	"relay-x/v1/relay"
	"sync"
	"time"
)

// Version: 1.0
// Developer: GPT-4/JOJO
// Date: 2026-02-15

type Reaper struct {
	pendingKills map[int]*time.Timer // port -> timer
	mu           sync.Mutex
}

var GlobalReaper = &Reaper{
	pendingKills: make(map[int]*time.Timer),
}

// ScheduleKill 安排 2秒后杀死端口
func (r *Reaper) ScheduleKill(ports []int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	
	timeout := config.GlobalConfig.Relay.ReaperTimeout
	if timeout == 0 {
		timeout = 2 * time.Second
	}
	
	for _, port := range ports {
		if _, ok := r.pendingKills[port]; ok {
			continue
		}
		
		p := port // capture loop var
		timer := time.AfterFunc(timeout, func() {
			r.executeKill(p)
		})
		r.pendingKills[port] = timer
		common.LogInfo("已安排熔断", "port", port, "timeout", timeout)
		
		// 立即停止转发逻辑 (Spec: "立即停止...并清洗缓冲区")
		// 但 "强制 Close()" 是在 2s 后。
		// 这里有个歧义：
		// "一旦...检测到...立即停止...并清洗"
		// "计时 2 秒...强制 Close()"
		// 这意味着先 StopSession (but keep ports open?), then Close Ports?
		// 但 StopSession 会 Close Connection。
		// 如果 Close Connection，Port 上的 Listener 还在吗？
		// RelayServer.StopPort 关闭 Listener 和 Session。
		// 如果只停止转发，可以只调用 Session.Stop()。
		// 但 Listener 应该保留以便重连？
		// Spec says "强制 Close() 关联的物理端口"。
		// So "立即停止" means stop data flow.
		// Let's stop the session now.
		if sess := relay.GlobalRelay.GetSession(port); sess != nil {
			sess.Stop()
		}
	}
}

// CancelKill 取消杀死端口 (重连成功)
func (r *Reaper) CancelKill(ports []int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	
	for _, port := range ports {
		if timer, ok := r.pendingKills[port]; ok {
			timer.Stop()
			delete(r.pendingKills, port)
			common.LogInfo("熔断已取消", "port", port)
		}
	}
}

func (r *Reaper) executeKill(port int) {
	r.mu.Lock()
	delete(r.pendingKills, port)
	r.mu.Unlock()
	
	common.LogWarn(fmt.Sprintf("[V1-熔断] 链路 %d 超时 2s，物理资源已强制回收", port))
	relay.GlobalRelay.StopPort(port)
}
