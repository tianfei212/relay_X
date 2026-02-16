package ports

import (
	"testing"
	"time"

	"relay-x/v4/status"
)

// TestPoolReserveOccupyRelease 验证端口从 Reserved -> Occupied -> Idle 的状态流转。
func TestPoolReserveOccupyRelease(t *testing.T) {
	p, err := NewPool(KindZMQ, 30000, 30002)
	if err != nil {
		t.Fatal(err)
	}

	a, err := p.Reserve("s1", "srv1", "cli1")
	if err != nil {
		t.Fatal(err)
	}
	if a.Port < 30000 || a.Port > 30002 {
		t.Fatalf("unexpected port: %d", a.Port)
	}

	snap := p.Snapshot()
	if snap.Reserved != 1 {
		t.Fatalf("reserved=%d", snap.Reserved)
	}
	if _, ok := p.Allocation(a.Port); !ok {
		t.Fatalf("expected allocation")
	}

	if err := p.MarkOccupied(a.Port, "s1"); err != nil {
		t.Fatal(err)
	}
	snap = p.Snapshot()
	if snap.Occupied != 1 {
		t.Fatalf("occupied=%d", snap.Occupied)
	}

	if err := p.Release(a.Port, "s1"); err != nil {
		t.Fatal(err)
	}
	if err := p.Release(a.Port, "s1"); err != nil {
		t.Fatal(err)
	}
	snap = p.Snapshot()
	if snap.Idle != snap.Total {
		t.Fatalf("idle=%d total=%d", snap.Idle, snap.Total)
	}
}

// TestPoolMiscPaths 覆盖端口池的一些边界路径（非法范围、未知端口等）。
func TestPoolMiscPaths(t *testing.T) {
	if _, err := NewPool(KindZMQ, 10, 1); err == nil {
		t.Fatalf("expected range error")
	}

	p, err := NewPool(KindSRT, 33000, 33001)
	if err != nil {
		t.Fatal(err)
	}
	if p.Kind() != KindSRT {
		t.Fatalf("kind=%s", p.Kind())
	}
	if err := p.Release(9999, ""); err == nil {
		t.Fatalf("expected unknown port error")
	}
	if err := p.MarkOccupied(33000, "x"); err == nil {
		t.Fatalf("expected not reserved error")
	}
}

// TestPoolReleaseExpiredReservations 验证 Reserved 过期释放回 Idle。
func TestPoolReleaseExpiredReservations(t *testing.T) {
	p, err := NewPool(KindZMQ, 31000, 31000)
	if err != nil {
		t.Fatal(err)
	}
	a, err := p.Reserve("s1", "srv1", "cli1")
	if err != nil {
		t.Fatal(err)
	}

	p.mu.Lock()
	x := p.alloc[a.Port]
	x.ReservedAt = time.Now().Add(-10 * time.Second)
	p.alloc[a.Port] = x
	p.state[a.Port] = status.PortReserved
	p.mu.Unlock()

	released := p.ReleaseExpiredReservations(3 * time.Second)
	if len(released) != 1 {
		t.Fatalf("released=%d", len(released))
	}
	if p.Snapshot().Idle != 1 {
		t.Fatalf("expected idle after release")
	}
}

// TestPoolConflictPaths 覆盖冲突/阻断等错误分支。
func TestPoolConflictPaths(t *testing.T) {
	p, err := NewPool(KindZMQ, 32000, 32000)
	if err != nil {
		t.Fatal(err)
	}
	a, err := p.Reserve("s1", "srv1", "cli1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Reserve("s2", "srv2", "cli2"); err == nil {
		t.Fatalf("expected no available port")
	}
	if err := p.MarkOccupied(a.Port, "s2"); err == nil {
		t.Fatalf("expected conflict")
	}
	if err := p.Release(a.Port, "s2"); err == nil {
		t.Fatalf("expected release conflict")
	}
	if err := p.Release(a.Port, ""); err != nil {
		t.Fatalf("expected release ok: %v", err)
	}
	if err := p.Block(9999, "x"); err == nil {
		t.Fatalf("expected block error")
	}
	if err := p.Block(a.Port, "x"); err != nil {
		t.Fatal(err)
	}
	snap := p.Snapshot()
	if snap.Blocked != 1 {
		t.Fatalf("blocked=%d", snap.Blocked)
	}
}
