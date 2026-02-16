package relay

import (
	"sync/atomic"
	"time"

	"relay-x/v4/ports"
)

type Metrics struct {
	Kind      ports.Kind
	Port      int
	SessionID string
	ServerID  string
	ClientID  string

	MbpsUp   float64
	MbpsDown float64
	RTTMs    float64
	LossPct  float64
	RetrPct  float64
	Quality  float64

	UpdatedAt time.Time
}

type counters struct {
	feIn  atomic.Int64
	feOut atomic.Int64
	beIn  atomic.Int64
	beOut atomic.Int64
}
