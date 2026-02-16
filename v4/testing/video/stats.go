package video

import (
	"encoding/json"
	"math"
	"sort"
	"time"
)

type Sample struct {
	Seq uint64 `json:"seq"`

	SendUnixNs       int64 `json:"send_unix_ns"`
	RecvUnixNs       int64 `json:"recv_unix_ns"`
	ServerRecvUnixNs int64 `json:"server_recv_unix_ns"`
	ServerSendUnixNs int64 `json:"server_send_unix_ns"`

	Bytes int `json:"bytes"`
}

type Report struct {
	Transport string `json:"transport"`
	File      string `json:"file"`

	StartedAtUnixNs  int64 `json:"started_at_unix_ns"`
	FinishedAtUnixNs int64 `json:"finished_at_unix_ns"`

	SentFrames int64 `json:"sent_frames"`
	SentBytes  int64 `json:"sent_bytes"`

	EchoFrames int64 `json:"echo_frames"`
	EchoBytes  int64 `json:"echo_bytes"`

	RTTMsP50 float64 `json:"rtt_ms_p50"`
	RTTMsP95 float64 `json:"rtt_ms_p95"`
	RTTMsP99 float64 `json:"rtt_ms_p99"`
	RTTMsMax float64 `json:"rtt_ms_max"`
	RTTMsAvg float64 `json:"rtt_ms_avg"`
	RTTMsStd float64 `json:"rtt_ms_std"`

	ServerProcMsP50 float64 `json:"server_proc_ms_p50"`
	ServerProcMsP95 float64 `json:"server_proc_ms_p95"`
	ServerProcMsP99 float64 `json:"server_proc_ms_p99"`

	GoodputMbpsUp   float64 `json:"goodput_mbps_up"`
	GoodputMbpsDown float64 `json:"goodput_mbps_down"`

	Samples []Sample `json:"samples,omitempty"`
}

type Collector struct {
	Transport   string
	File        string
	KeepSamples bool

	started  time.Time
	finished time.Time

	sentFrames int64
	sentBytes  int64

	echoFrames int64
	echoBytes  int64

	rttMs    []float64
	serverMs []float64

	samples []Sample
}

func (c *Collector) Start() {
	c.started = time.Now()
}

func (c *Collector) Finish() {
	c.finished = time.Now()
}

func (c *Collector) OnSend(n int) {
	c.sentFrames++
	c.sentBytes += int64(n)
}

func (c *Collector) OnEcho(seq uint64, sendUnixNs, serverRecvUnixNs, serverSendUnixNs, recvUnixNs int64, n int) {
	c.echoFrames++
	c.echoBytes += int64(n)

	rtt := float64(recvUnixNs-sendUnixNs) / 1e6
	if rtt >= 0 {
		c.rttMs = append(c.rttMs, rtt)
	}
	if serverRecvUnixNs > 0 && serverSendUnixNs >= serverRecvUnixNs {
		c.serverMs = append(c.serverMs, float64(serverSendUnixNs-serverRecvUnixNs)/1e6)
	}

	if c.KeepSamples {
		c.samples = append(c.samples, Sample{
			Seq:              seq,
			SendUnixNs:       sendUnixNs,
			RecvUnixNs:       recvUnixNs,
			ServerRecvUnixNs: serverRecvUnixNs,
			ServerSendUnixNs: serverSendUnixNs,
			Bytes:            n,
		})
	}
}

func (c *Collector) BuildReport() Report {
	r := Report{
		Transport:        c.Transport,
		File:             c.File,
		StartedAtUnixNs:  c.started.UnixNano(),
		FinishedAtUnixNs: c.finished.UnixNano(),
		SentFrames:       c.sentFrames,
		SentBytes:        c.sentBytes,
		EchoFrames:       c.echoFrames,
		EchoBytes:        c.echoBytes,
	}

	d := c.finished.Sub(c.started).Seconds()
	if d > 0 {
		r.GoodputMbpsUp = float64(c.sentBytes*8) / 1e6 / d
		r.GoodputMbpsDown = float64(c.echoBytes*8) / 1e6 / d
	}

	r.RTTMsP50, r.RTTMsP95, r.RTTMsP99, r.RTTMsMax, r.RTTMsAvg, r.RTTMsStd = summarize(c.rttMs)
	r.ServerProcMsP50, r.ServerProcMsP95, r.ServerProcMsP99, _, _, _ = summarize(c.serverMs)

	if c.KeepSamples {
		r.Samples = append(r.Samples, c.samples...)
	}
	return r
}

func (r Report) MarshalIndent() ([]byte, error) {
	return json.MarshalIndent(r, "", "  ")
}

func summarize(vals []float64) (p50, p95, p99, maxv, avg, std float64) {
	if len(vals) == 0 {
		return 0, 0, 0, 0, 0, 0
	}
	cp := append([]float64(nil), vals...)
	sort.Float64s(cp)
	maxv = cp[len(cp)-1]
	p50 = quantileSorted(cp, 0.50)
	p95 = quantileSorted(cp, 0.95)
	p99 = quantileSorted(cp, 0.99)
	var sum float64
	for _, v := range cp {
		sum += v
	}
	avg = sum / float64(len(cp))
	var ss float64
	for _, v := range cp {
		d := v - avg
		ss += d * d
	}
	std = math.Sqrt(ss / float64(len(cp)))
	return p50, p95, p99, maxv, avg, std
}

func quantileSorted(sorted []float64, q float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	if q <= 0 {
		return sorted[0]
	}
	if q >= 1 {
		return sorted[len(sorted)-1]
	}
	pos := q * float64(len(sorted)-1)
	i := int(pos)
	f := pos - float64(i)
	if i+1 >= len(sorted) {
		return sorted[i]
	}
	return sorted[i]*(1-f) + sorted[i+1]*f
}
