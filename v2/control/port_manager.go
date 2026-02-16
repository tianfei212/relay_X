package control

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	relayv2 "relay-x/v2/relay"
	"sync"
	"time"
)

type portReservation struct {
	owner     *Client
	requestID string
	sessionID string
	zmqPort   int
	srtPort   int
	expiresAt time.Time
}

type PortManager struct {
	mu sync.Mutex

	zmqPorts []int
	srtPorts []int

	resvBySession map[string]*portReservation
	resvByPort    map[int]string
	resvByOwner   map[*Client]map[string]struct{}
}

var GlobalPortManager = NewPortManager()

func NewPortManager() *PortManager {
	pm := &PortManager{
		resvBySession: make(map[string]*portReservation),
		resvByPort:    make(map[int]string),
		resvByOwner:   make(map[*Client]map[string]struct{}),
	}
	relayv2.GlobalRelay.SetOnPortFree(pm.ReleaseByPort)
	go pm.sweepLoop()
	return pm
}

func (pm *PortManager) SetRanges(p PortAllocPayload) (int, int) {
	pm.mu.Lock()
	defer pm.mu.Unlock()

	pm.zmqPorts = pm.zmqPorts[:0]
	if p.ZMQStartPort <= p.ZMQEndPort {
		pm.zmqPorts = make([]int, 0, p.ZMQEndPort-p.ZMQStartPort+1)
		for port := p.ZMQStartPort; port <= p.ZMQEndPort; port++ {
			pm.zmqPorts = append(pm.zmqPorts, port)
		}
	}

	pm.srtPorts = pm.srtPorts[:0]
	if p.SRTStartPort <= p.SRTEndPort {
		pm.srtPorts = make([]int, 0, p.SRTEndPort-p.SRTStartPort+1)
		for port := p.SRTStartPort; port <= p.SRTEndPort; port++ {
			pm.srtPorts = append(pm.srtPorts, port)
		}
	}

	return len(pm.zmqPorts), len(pm.srtPorts)
}

func (pm *PortManager) Acquire(owner *Client, req PortAcquirePayload) (PortGrantPayload, error) {
	if owner == nil {
		return PortGrantPayload{}, fmt.Errorf("owner不能为空")
	}
	if !req.NeedZMQ && !req.NeedSRT {
		return PortGrantPayload{}, fmt.Errorf("need_zmq/need_srt至少一个为true")
	}
	if req.NeedZMQ && req.NeedSRT {
		return PortGrantPayload{}, fmt.Errorf("一次只能申请一种协议端口(need_zmq或need_srt)")
	}

	ttl := time.Duration(req.TTLSeconds) * time.Second
	if ttl <= 0 {
		ttl = 30 * time.Second
	}

	pm.mu.Lock()
	defer pm.mu.Unlock()

	now := time.Now()
	pm.sweepLocked(now)

	var zmqPort, srtPort int
	if req.NeedZMQ {
		p, ok := pm.pickFreePortLocked(pm.zmqPorts)
		if !ok {
			return PortGrantPayload{}, fmt.Errorf("无可用zmq端口")
		}
		zmqPort = p
	} else {
		p, ok := pm.pickFreePortLocked(pm.srtPorts)
		if !ok {
			return PortGrantPayload{}, fmt.Errorf("无可用srt端口")
		}
		srtPort = p
	}

	sid := newSessionID()
	expiresAt := now.Add(ttl)
	resv := &portReservation{
		owner:     owner,
		requestID: req.RequestID,
		sessionID: sid,
		zmqPort:   zmqPort,
		srtPort:   srtPort,
		expiresAt: expiresAt,
	}

	pm.resvBySession[sid] = resv
	if zmqPort > 0 {
		pm.resvByPort[zmqPort] = sid
	}
	if srtPort > 0 {
		pm.resvByPort[srtPort] = sid
	}
	if _, ok := pm.resvByOwner[owner]; !ok {
		pm.resvByOwner[owner] = make(map[string]struct{})
	}
	pm.resvByOwner[owner][sid] = struct{}{}

	return PortGrantPayload{
		RequestID: req.RequestID,
		SessionID: sid,
		ZMQPort:   zmqPort,
		SRTPort:   srtPort,
		ExpiresAt: expiresAt.UnixMilli(),
	}, nil
}

func (pm *PortManager) AcquireFromReady(owner *Client, req PortAcquirePayload) (PortGrantPayload, error) {
	if owner == nil {
		return PortGrantPayload{}, fmt.Errorf("owner不能为空")
	}
	if !req.NeedZMQ && !req.NeedSRT {
		return PortGrantPayload{}, fmt.Errorf("need_zmq/need_srt至少一个为true")
	}
	if req.NeedZMQ && req.NeedSRT {
		return PortGrantPayload{}, fmt.Errorf("一次只能申请一种协议端口(need_zmq或need_srt)")
	}

	ttl := time.Duration(req.TTLSeconds) * time.Second
	if ttl <= 0 {
		ttl = 30 * time.Second
	}

	pm.mu.Lock()
	defer pm.mu.Unlock()

	now := time.Now()
	pm.sweepLocked(now)

	snap := relayv2.GlobalRelay.SnapshotPortState()
	var candidates []int
	if req.NeedZMQ {
		candidates = snap.ZMQReadyPorts
	} else {
		candidates = snap.SRTReadyPorts
	}
	if len(candidates) == 0 {
		if req.NeedZMQ {
			return PortGrantPayload{}, fmt.Errorf("无可用已激活zmq端口")
		}
		return PortGrantPayload{}, fmt.Errorf("无可用已激活srt端口")
	}

	var zmqPort, srtPort int
	for _, p := range candidates {
		if p <= 0 {
			continue
		}
		if _, reserved := pm.resvByPort[p]; reserved {
			continue
		}
		if relayv2.GlobalRelay.IsPortBusy(p) {
			continue
		}
		if req.NeedZMQ {
			zmqPort = p
		} else {
			srtPort = p
		}
		break
	}
	if zmqPort == 0 && srtPort == 0 {
		if req.NeedZMQ {
			return PortGrantPayload{}, fmt.Errorf("无可用已激活zmq端口")
		}
		return PortGrantPayload{}, fmt.Errorf("无可用已激活srt端口")
	}

	sid := newSessionID()
	expiresAt := now.Add(ttl)
	resv := &portReservation{
		owner:     owner,
		requestID: req.RequestID,
		sessionID: sid,
		zmqPort:   zmqPort,
		srtPort:   srtPort,
		expiresAt: expiresAt,
	}

	pm.resvBySession[sid] = resv
	if zmqPort > 0 {
		pm.resvByPort[zmqPort] = sid
	}
	if srtPort > 0 {
		pm.resvByPort[srtPort] = sid
	}
	if _, ok := pm.resvByOwner[owner]; !ok {
		pm.resvByOwner[owner] = make(map[string]struct{})
	}
	pm.resvByOwner[owner][sid] = struct{}{}

	return PortGrantPayload{
		RequestID: req.RequestID,
		SessionID: sid,
		ZMQPort:   zmqPort,
		SRTPort:   srtPort,
		ExpiresAt: expiresAt.UnixMilli(),
	}, nil
}

func (pm *PortManager) Release(owner *Client, sessionID string) bool {
	if owner == nil || sessionID == "" {
		return false
	}
	pm.mu.Lock()
	defer pm.mu.Unlock()

	resv, ok := pm.resvBySession[sessionID]
	if !ok || resv == nil || resv.owner != owner {
		return false
	}
	pm.deleteReservationLocked(resv)
	return true
}

func (pm *PortManager) ReleaseAllForOwner(owner *Client) int {
	if owner == nil {
		return 0
	}
	pm.mu.Lock()
	defer pm.mu.Unlock()

	set := pm.resvByOwner[owner]
	if len(set) == 0 {
		return 0
	}

	n := 0
	for sid := range set {
		if resv, ok := pm.resvBySession[sid]; ok && resv != nil {
			pm.deleteReservationLocked(resv)
			n++
		}
	}
	delete(pm.resvByOwner, owner)
	return n
}

func (pm *PortManager) ReleaseByPort(port int) {
	if port <= 0 {
		return
	}
	pm.mu.Lock()
	defer pm.mu.Unlock()

	sid, ok := pm.resvByPort[port]
	if !ok || sid == "" {
		return
	}
	resv, ok := pm.resvBySession[sid]
	if !ok || resv == nil {
		delete(pm.resvByPort, port)
		return
	}
	pm.deleteReservationLocked(resv)
}

func (pm *PortManager) pickFreePortLocked(candidates []int) (int, bool) {
	for _, p := range candidates {
		if p <= 0 {
			continue
		}
		if _, reserved := pm.resvByPort[p]; reserved {
			continue
		}
		if relayv2.GlobalRelay.IsPortBusy(p) {
			continue
		}
		return p, true
	}
	return 0, false
}

func (pm *PortManager) sweepLoop() {
	t := time.NewTicker(1 * time.Second)
	defer t.Stop()
	for range t.C {
		pm.mu.Lock()
		pm.sweepLocked(time.Now())
		pm.mu.Unlock()
	}
}

func (pm *PortManager) sweepLocked(now time.Time) {
	for _, resv := range pm.resvBySession {
		if resv == nil {
			continue
		}
		if !resv.expiresAt.IsZero() && now.After(resv.expiresAt) {
			pm.deleteReservationLocked(resv)
		}
	}
}

func (pm *PortManager) deleteReservationLocked(resv *portReservation) {
	delete(pm.resvBySession, resv.sessionID)
	if resv.zmqPort > 0 {
		delete(pm.resvByPort, resv.zmqPort)
	}
	if resv.srtPort > 0 {
		delete(pm.resvByPort, resv.srtPort)
	}
	if set, ok := pm.resvByOwner[resv.owner]; ok {
		delete(set, resv.sessionID)
		if len(set) == 0 {
			delete(pm.resvByOwner, resv.owner)
		}
	}
}

func newSessionID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b)
}
