package control

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"relay-x/v1/config"
	relayv3 "relay-x/v3/relay"
	"sort"
	"sync"
	"time"
)

type PortManager struct {
	mu sync.Mutex

	ports []int

	resvBySession map[string]*portReservation
	resvByPort    map[int]*portReservation
	resvByOwner   map[*Client]map[string]*portReservation
}

type portReservation struct {
	sessionID string
	port      int
	owner     *Client
	expiresAt time.Time
}

var GlobalPortManager = &PortManager{
	resvBySession: make(map[string]*portReservation),
	resvByPort:    make(map[int]*portReservation),
	resvByOwner:   make(map[*Client]map[string]*portReservation),
}

func (pm *PortManager) SetRanges(p PortAllocPayload) {
	pm.mu.Lock()
	defer pm.mu.Unlock()

	ports := unionPortsFromRanges(p)
	if len(ports) == 0 {
		ports = unionPortsFromConfig()
	}
	sort.Ints(ports)
	pm.ports = ports
}

func (pm *PortManager) AcquireFromReady(owner *Client, req PortAcquirePayload) (PortGrantPayload, error) {
	snap := relayv3.GlobalRelay.SnapshotPortState()
	for _, p := range snap.ReadyPorts {
		if pm.isPortAvailable(p) {
			return pm.reservePort(owner, req, p), nil
		}
	}
	return pm.Acquire(owner, req)
}

func (pm *PortManager) Acquire(owner *Client, req PortAcquirePayload) (PortGrantPayload, error) {
	snap := relayv3.GlobalRelay.SnapshotPortState()
	for _, p := range snap.EmptyPorts {
		if pm.isPortAvailable(p) {
			return pm.reservePort(owner, req, p), nil
		}
	}
	return PortGrantPayload{}, fmt.Errorf("no available ports")
}

func (pm *PortManager) Release(owner *Client, sessionID string) (released bool, port int) {
	pm.mu.Lock()
	defer pm.mu.Unlock()

	resv := pm.resvBySession[sessionID]
	if resv == nil {
		return false, 0
	}
	if resv.owner != nil && owner != nil && resv.owner != owner {
		return false, 0
	}
	port = resv.port
	pm.deleteReservationLocked(resv)
	return true, port
}

func (pm *PortManager) ReleaseAllForOwner(owner *Client) {
	if owner == nil {
		return
	}
	pm.mu.Lock()
	defer pm.mu.Unlock()

	set := pm.resvByOwner[owner]
	for _, resv := range set {
		pm.deleteReservationLocked(resv)
	}
}

func (pm *PortManager) isPortAvailable(port int) bool {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	if _, ok := pm.resvByPort[port]; ok {
		return false
	}
	return true
}

func (pm *PortManager) reservePort(owner *Client, req PortAcquirePayload, port int) PortGrantPayload {
	pm.mu.Lock()
	defer pm.mu.Unlock()

	ttl := req.TTLSeconds
	if ttl <= 0 {
		ttl = 60
	}
	expiresAt := time.Now().Add(time.Duration(ttl) * time.Second)

	sid := newSessionID()
	resv := &portReservation{
		sessionID: sid,
		port:      port,
		owner:     owner,
		expiresAt: expiresAt,
	}
	pm.resvBySession[sid] = resv
	pm.resvByPort[port] = resv
	if owner != nil {
		if _, ok := pm.resvByOwner[owner]; !ok {
			pm.resvByOwner[owner] = make(map[string]*portReservation)
		}
		pm.resvByOwner[owner][sid] = resv
	}

	return PortGrantPayload{
		RequestID: req.RequestID,
		SessionID: sid,
		ZMQPort:   port,
		SRTPort:   port,
		ExpiresAt: expiresAt.UnixMilli(),
	}
}

func (pm *PortManager) sweepLoop() {
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	for range ticker.C {
		now := time.Now()
		pm.mu.Lock()
		for _, resv := range pm.resvBySession {
			if resv == nil {
				continue
			}
			if !resv.expiresAt.IsZero() && now.After(resv.expiresAt) {
				pm.deleteReservationLocked(resv)
			}
		}
		pm.mu.Unlock()
	}
}

func (pm *PortManager) deleteReservationLocked(resv *portReservation) {
	delete(pm.resvBySession, resv.sessionID)
	delete(pm.resvByPort, resv.port)
	if set, ok := pm.resvByOwner[resv.owner]; ok {
		delete(set, resv.sessionID)
		if len(set) == 0 {
			delete(pm.resvByOwner, resv.owner)
		}
	}
}

func newSessionID() string {
	var b [12]byte
	if _, err := rand.Read(b[:]); err != nil {
		return hex.EncodeToString([]byte(fmt.Sprintf("%d", time.Now().UnixNano())))
	}
	return hex.EncodeToString(b[:])
}

func unionPortsFromRanges(p PortAllocPayload) []int {
	var out []int
	addRange := func(start, end int) {
		if start <= 0 || end <= 0 || start > end {
			return
		}
		for i := start; i <= end; i++ {
			out = append(out, i)
		}
	}
	addRange(p.ZMQStartPort, p.ZMQEndPort)
	addRange(p.SRTStartPort, p.SRTEndPort)
	return dedupInts(out)
}

func unionPortsFromConfig() []int {
	p := PortAllocPayload{
		ZMQStartPort: config.GlobalConfig.Relay.ZMQStartPort,
		ZMQEndPort:   config.GlobalConfig.Relay.ZMQEndPort,
		SRTStartPort: config.GlobalConfig.Relay.SRTStartPort,
		SRTEndPort:   config.GlobalConfig.Relay.SRTEndPort,
	}
	return unionPortsFromRanges(p)
}

func dedupInts(in []int) []int {
	if len(in) == 0 {
		return nil
	}
	sort.Ints(in)
	out := in[:0]
	var last int
	for i, v := range in {
		if i == 0 || v != last {
			out = append(out, v)
			last = v
		}
	}
	return out
}

func (pm *PortManager) ensureDefaultRanges() {
	pm.mu.Lock()
	has := len(pm.ports) > 0
	pm.mu.Unlock()
	if has {
		return
	}
	pm.SetRanges(PortAllocPayload{})
}

func InitPortManager() {
	GlobalPortManager.ensureDefaultRanges()
	go GlobalPortManager.sweepLoop()
}
