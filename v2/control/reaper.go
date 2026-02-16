package control

import (
	"fmt"
	"relay-x/v1/common"
	"relay-x/v1/config"
	relayv2 "relay-x/v2/relay"
	"sync"
	"time"
)

type Reaper struct {
	pendingKills map[int]*time.Timer
	mu           sync.Mutex
}

var GlobalReaper = &Reaper{
	pendingKills: make(map[int]*time.Timer),
}

// ScheduleKill 安排一段时间后杀死端口
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

		p := port
		timer := time.AfterFunc(timeout, func() {
			r.executeKill(p)
		})
		r.pendingKills[port] = timer
		common.LogInfo("已安排熔断", "port", port, "timeout", timeout)

		if sess := relayv2.GlobalRelay.GetSession(port); sess != nil {
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

	common.LogWarn(fmt.Sprintf("[V2-熔断] 链路 %d 超时，物理资源已强制回收", port))
	relayv2.GlobalRelay.StopPort(port)
}
