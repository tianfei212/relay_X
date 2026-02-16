package ports

import (
	"fmt"
	"sort"
	"sync"
	"time"

	v4errors "relay-x/v4/errors"
	"relay-x/v4/status"
)

type Kind string

const (
	KindZMQ Kind = "zmq"
	KindSRT Kind = "srt"
)

type Allocation struct {
	Kind       Kind
	Port       int
	SessionID  string
	ServerID   string
	ClientID   string
	ReservedAt time.Time
}

type Pool struct {
	kind  Kind
	ports []int

	mu    sync.RWMutex
	state map[int]status.PortStatus
	alloc map[int]Allocation
}

// NewPool 创建一个端口池。
// 参数：
// - kind: 端口类型（zmq/srt）
// - start: 起始端口（含）
// - end: 结束端口（含）
// 返回：
// - *Pool: 端口池实例
// - error: 端口范围非法时返回错误
func NewPool(kind Kind, start, end int) (*Pool, error) {
	if start <= 0 || end <= 0 || end < start || end > 65535 {
		return nil, fmt.Errorf("invalid port range: %d-%d", start, end)
	}
	p := &Pool{
		kind:  kind,
		state: make(map[int]status.PortStatus),
		alloc: make(map[int]Allocation),
	}
	for i := start; i <= end; i++ {
		p.ports = append(p.ports, i)
		p.state[i] = status.PortIdle
	}
	return p, nil
}

// Kind 返回端口池类型。
func (p *Pool) Kind() Kind { return p.kind }

// Reserve 从端口池中挑选一个空闲端口并标记为 Reserved。
// 参数：
// - sessionID: 会话 ID
// - serverID: 后端 ID
// - clientID: 前端 ID
// 返回：
// - Allocation: 分配结果（包含端口与绑定关系）
// - error: 无可用端口时返回 CodeUnavailable
func (p *Pool) Reserve(sessionID, serverID, clientID string) (Allocation, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	for _, port := range p.ports {
		if p.state[port] != status.PortIdle {
			continue
		}
		a := Allocation{
			Kind:       p.kind,
			Port:       port,
			SessionID:  sessionID,
			ServerID:   serverID,
			ClientID:   clientID,
			ReservedAt: time.Now(),
		}
		p.state[port] = status.PortReserved
		p.alloc[port] = a
		return a, nil
	}
	return Allocation{}, v4errors.New(v4errors.CodeUnavailable, "no available port")
}

// MarkOccupied 将端口从 Reserved 标记为 Occupied。
// 参数：
// - port: 端口号
// - sessionID: 会话 ID（必须与 Reserve 时一致）
// 返回：
// - error: 端口非 Reserved 或会话不匹配时返回冲突错误
func (p *Pool) MarkOccupied(port int, sessionID string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.state[port] != status.PortReserved {
		return v4errors.Wrap(v4errors.CodeConflict, "port not reserved", fmt.Errorf("port=%d state=%s", port, p.state[port]))
	}
	a := p.alloc[port]
	if a.SessionID != sessionID {
		return v4errors.Wrap(v4errors.CodeConflict, "port reserved by another session", fmt.Errorf("port=%d", port))
	}
	p.state[port] = status.PortOccupied
	return nil
}

// Release 释放端口，将其标记回 Idle。
// 参数：
// - port: 端口号
// - sessionID: 会话 ID（可为空；为空时强制释放）
// 返回：
// - error: 端口未知或会话冲突时返回错误
func (p *Pool) Release(port int, sessionID string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	cur := p.state[port]
	if cur == "" {
		return v4errors.Wrap(v4errors.CodeBadRequest, "unknown port", fmt.Errorf("port=%d", port))
	}
	if cur == status.PortIdle {
		return nil
	}
	a := p.alloc[port]
	if sessionID != "" && a.SessionID != sessionID {
		return v4errors.Wrap(v4errors.CodeConflict, "release conflict", fmt.Errorf("port=%d", port))
	}
	delete(p.alloc, port)
	p.state[port] = status.PortIdle
	return nil
}

// Block 将端口标记为 Blocked（用于隔离异常端口）。
// 参数：
// - port: 端口号
// - reason: 阻断原因（当前未持久化，仅用于调用侧记录）
// 返回：
// - error: 端口未知时返回错误
func (p *Pool) Block(port int, reason string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if _, ok := p.state[port]; !ok {
		return v4errors.Wrap(v4errors.CodeBadRequest, "unknown port", fmt.Errorf("port=%d", port))
	}
	delete(p.alloc, port)
	p.state[port] = status.PortBlocked
	return nil
}

// Snapshot 返回端口池的快照统计与各状态端口列表。
func (p *Pool) Snapshot() Snapshot {
	p.mu.RLock()
	defer p.mu.RUnlock()

	var s Snapshot
	s.Kind = p.kind
	for _, port := range p.ports {
		switch p.state[port] {
		case status.PortIdle:
			s.Idle++
			s.IdlePorts = append(s.IdlePorts, port)
		case status.PortReserved:
			s.Reserved++
			s.ReservedPorts = append(s.ReservedPorts, port)
		case status.PortOccupied:
			s.Occupied++
			s.OccupiedPorts = append(s.OccupiedPorts, port)
		case status.PortBlocked:
			s.Blocked++
			s.BlockedPorts = append(s.BlockedPorts, port)
		}
	}
	sort.Ints(s.IdlePorts)
	sort.Ints(s.ReservedPorts)
	sort.Ints(s.OccupiedPorts)
	sort.Ints(s.BlockedPorts)
	s.Total = len(p.ports)
	return s
}

type Snapshot struct {
	Kind Kind

	Total    int
	Idle     int
	Reserved int
	Occupied int
	Blocked  int

	IdlePorts     []int
	ReservedPorts []int
	OccupiedPorts []int
	BlockedPorts  []int
}

// Allocation 查询指定端口的分配信息。
// 返回：
// - Allocation: 分配信息
// - bool: 是否存在
func (p *Pool) Allocation(port int) (Allocation, bool) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	a, ok := p.alloc[port]
	return a, ok
}

func (p *Pool) TouchReservation(port int, sessionID string) {
	if port <= 0 || sessionID == "" {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.state[port] != status.PortReserved {
		return
	}
	a, ok := p.alloc[port]
	if !ok {
		return
	}
	if a.SessionID != sessionID {
		return
	}
	a.ReservedAt = time.Now()
	p.alloc[port] = a
}

// ReleaseExpiredReservations 释放超过 maxAge 的 Reserved 端口，并返回被释放的分配信息列表。
// 参数：
// - maxAge: 保留超时时间
// 返回：
// - []Allocation: 被释放的分配记录
func (p *Pool) ReleaseExpiredReservations(maxAge time.Duration) []Allocation {
	now := time.Now()
	var released []Allocation

	p.mu.Lock()
	defer p.mu.Unlock()

	for port, st := range p.state {
		if st != status.PortReserved {
			continue
		}
		a := p.alloc[port]
		if a.SessionID == "" {
			p.state[port] = status.PortIdle
			delete(p.alloc, port)
			continue
		}
		if now.Sub(a.ReservedAt) > maxAge {
			released = append(released, a)
			p.state[port] = status.PortIdle
			delete(p.alloc, port)
		}
	}

	return released
}
