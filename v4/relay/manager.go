package relay

import (
	"context"
	"sync"
	"time"

	srt "github.com/datarhei/gosrt"

	"relay-x/v4/config"
	v4log "relay-x/v4/log"
	"relay-x/v4/ports"
)

type Manager struct {
	cfg config.Config

	ZMQPool *ports.Pool
	SRTPool *ports.Pool

	zmq *tcpServer
	srt *srtServer

	ctx    context.Context
	cancel context.CancelFunc

	wg sync.WaitGroup

	mu       sync.RWMutex
	metrics  map[string]Metrics
	prevCtrs map[string][4]int64
}

// NewManager 创建数据平面管理器。
// 职责：
// - 启动/停止 ZMQ(TCP) 与 SRT 数据端口监听
// - 维护会话转发与指标采集
// - 定期执行保留超时回收与心跳超时关闭
// 参数：
// - cfg: 全局配置
// - zmqPool/srtPool: 端口池
func NewManager(cfg config.Config, zmqPool, srtPool *ports.Pool) *Manager {
	m := &Manager{
		cfg:      cfg,
		ZMQPool:  zmqPool,
		SRTPool:  srtPool,
		metrics:  make(map[string]Metrics),
		prevCtrs: make(map[string][4]int64),
	}
	m.zmq = newTCPServer(ports.KindZMQ, zmqPool, cfg.ZeroMQ)

	scfg := srt.DefaultConfig()
	scfg.Latency = time.Duration(cfg.GoSRT.Latency) * time.Millisecond
	scfg.MaxBW = cfg.GoSRT.MaxBW
	scfg.IPTTL = cfg.GoSRT.IPTTL
	scfg.PeerIdleTimeout = 8 * time.Second
	m.srt = newSRTServer(srtPool, scfg)
	return m
}

// Start 启动数据平面（监听所有配置端口）以及后台指标/心跳任务。
// 返回：
// - error: 启动监听失败原因
func (m *Manager) Start() error {
	m.ctx, m.cancel = context.WithCancel(context.Background())

	zmqRange, _ := config.ParsePortRange(m.cfg.ZeroMQ.PortRange)
	for p := zmqRange.Start; p <= zmqRange.End; p++ {
		if err := m.zmq.StartPort(m.ctx, p); err != nil {
			return err
		}
	}
	srtRange, _ := config.ParsePortRange(m.cfg.GoSRT.PortRange)
	for p := srtRange.Start; p <= srtRange.End; p++ {
		if err := m.srt.StartPort(m.ctx, p); err != nil {
			return err
		}
	}

	m.wg.Add(2)
	go func() {
		defer m.wg.Done()
		m.metricsLoop()
	}()
	go func() {
		defer m.wg.Done()
		m.heartbeatLoop()
	}()

	return nil
}

// Stop 停止数据平面并等待后台任务退出（幂等）。
func (m *Manager) Stop() {
	if m.cancel != nil {
		m.cancel()
	}
	m.zmq.StopAll()
	m.srt.StopAll()
	m.wg.Wait()
}

// SnapshotMetrics 返回当前指标快照（用于控制面遥测广播）。
func (m *Manager) SnapshotMetrics() []Metrics {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]Metrics, 0, len(m.metrics))
	for _, v := range m.metrics {
		out = append(out, v)
	}
	return out
}

// metricsLoop 周期性更新指标缓存（1s 一次）。
func (m *Manager) metricsLoop() {
	t := time.NewTicker(1 * time.Second)
	defer t.Stop()

	for {
		select {
		case <-m.ctx.Done():
			return
		case <-t.C:
			m.updateMetrics()
		}
	}
}

// updateMetrics 扫描当前会话并汇总 Mbps/RTT/丢包等指标。
func (m *Manager) updateMetrics() {
	now := time.Now()

	mm := make(map[string]Metrics)

	for _, sess := range m.zmq.Sessions() {
		feIn, feOut, beIn, beOut := sess.Counters()
		key := sess.sessionID + ":zmq:" + itoa(sess.port)
		prev := m.prevCtrs[key]
		deltaIn := (feIn + beIn) - (prev[0] + prev[2])
		deltaOut := (feOut + beOut) - (prev[1] + prev[3])
		m.prevCtrs[key] = [4]int64{feIn, feOut, beIn, beOut}

		mm[key] = Metrics{
			Kind:      ports.KindZMQ,
			Port:      sess.port,
			SessionID: sess.sessionID,
			ServerID:  sess.serverID,
			ClientID:  sess.clientID,
			MbpsUp:    float64(deltaIn*8) / 1e6,
			MbpsDown:  float64(deltaOut*8) / 1e6,
			RTTMs:     0,
			LossPct:   0,
			RetrPct:   0,
			Quality:   qualityScore(float64(deltaIn*8)/1e6, float64(deltaOut*8)/1e6, 0, 0),
			UpdatedAt: now,
		}
	}

	for _, st := range m.srt.SnapshotStats() {
		key := st.SessionID + ":srt:" + itoa(st.Port)
		mm[key] = st
	}

	m.mu.Lock()
	m.metrics = mm
	m.mu.Unlock()
}

// heartbeatLoop 周期性执行心跳检测与超时回收（3s 一次）。
func (m *Manager) heartbeatLoop() {
	t := time.NewTicker(3 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-m.ctx.Done():
			return
		case <-t.C:
			m.checkHeartbeats()
		}
	}
}

// checkHeartbeats 释放过期保留端口，并对读超时会话执行关闭。
func (m *Manager) checkHeartbeats() {
	expiredZ := m.ZMQPool.ReleaseExpiredReservations(m.cfg.Gateway.AuthTimeout)
	for _, a := range expiredZ {
		v4log.With(map[string]any{"port": a.Port, "sessionID": a.SessionID, "status": "reservation_expired"}).Warn("ZMQ 端口保留已过期并回收")
	}
	expiredS := m.SRTPool.ReleaseExpiredReservations(m.cfg.Gateway.AuthTimeout)
	for _, a := range expiredS {
		v4log.With(map[string]any{"port": a.Port, "sessionID": a.SessionID, "status": "reservation_expired"}).Warn("SRT 端口保留已过期并回收")
	}

	for _, sess := range m.zmq.Sessions() {
		feAge, beAge := sess.LastReadAges()
		if feAge > 6*time.Second || beAge > 6*time.Second {
			v4log.With(map[string]any{
				"port":      sess.port,
				"sessionID": sess.sessionID,
				"status":    "heartbeat_timeout",
				"fe_age_ms": feAge.Milliseconds(),
				"be_age_ms": beAge.Milliseconds(),
			}).Warn("TCP 心跳超时，关闭会话")
			sess.Stop()
		}
	}

	for _, sess := range m.srt.Sessions() {
		feAge, beAge := sess.LastReadAges()
		if feAge > 6*time.Second || beAge > 6*time.Second {
			v4log.With(map[string]any{
				"port":      sess.port,
				"sessionID": sess.sessionID,
				"status":    "heartbeat_timeout",
				"fe_age_ms": feAge.Milliseconds(),
				"be_age_ms": beAge.Milliseconds(),
			}).Warn("SRT 心跳超时，关闭会话")
			sess.Stop()
		}
	}
}

// itoa 将 int 转为十进制字符串（避免引入 fmt 依赖开销）。
func itoa(v int) string {
	if v == 0 {
		return "0"
	}
	neg := v < 0
	if neg {
		v = -v
	}
	var b [16]byte
	i := len(b)
	for v > 0 {
		i--
		b[i] = byte('0' + v%10)
		v /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}
