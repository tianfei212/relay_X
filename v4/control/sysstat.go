package control

import (
	"bufio"
	"fmt"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync"
)

type sysSampler struct {
	mu sync.Mutex

	lastTotal uint64
	lastIdle  uint64
}

// newSysSampler 创建系统资源采样器。
func newSysSampler() *sysSampler { return &sysSampler{} }

// CPUPercent 返回基于 /proc/stat 计算的 CPU 使用率（0~100）。
func (s *sysSampler) CPUPercent() float64 {
	s.mu.Lock()
	defer s.mu.Unlock()

	total, idle, err := readProcStat()
	if err != nil {
		return 0
	}
	if s.lastTotal == 0 {
		s.lastTotal = total
		s.lastIdle = idle
		return 0
	}
	dt := total - s.lastTotal
	di := idle - s.lastIdle
	s.lastTotal = total
	s.lastIdle = idle
	if dt == 0 {
		return 0
	}
	busy := float64(dt-di) / float64(dt) * 100
	if busy < 0 {
		return 0
	}
	if busy > 100 {
		return 100
	}
	return busy
}

// MemMB 返回当前进程内存使用量（runtime.MemStats.Alloc），单位 MB。
func (s *sysSampler) MemMB() float64 {
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	return float64(ms.Alloc) / (1024 * 1024)
}

// readProcStat 读取 /proc/stat 首行 cpu 统计并返回 total 与 idle 时间片。
func readProcStat() (total, idle uint64, err error) {
	f, err := os.Open("/proc/stat")
	if err != nil {
		return 0, 0, err
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	if !sc.Scan() {
		return 0, 0, fmt.Errorf("empty /proc/stat")
	}
	line := sc.Text()
	fields := strings.Fields(line)
	if len(fields) < 5 || fields[0] != "cpu" {
		return 0, 0, fmt.Errorf("invalid /proc/stat header")
	}
	var vals []uint64
	for _, f := range fields[1:] {
		v, e := strconv.ParseUint(f, 10, 64)
		if e != nil {
			return 0, 0, e
		}
		vals = append(vals, v)
	}
	var sum uint64
	for _, v := range vals {
		sum += v
	}
	idle = vals[3]
	if len(vals) > 4 {
		idle += vals[4]
	}
	return sum, idle, nil
}
