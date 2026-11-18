package eventreaper

import (
	"sync"
	"time"

	"kas-core-pilot/internal/metrics"
)

// Stats keeps cumulative counters for /metrics. Labels come from fixed sets.
type Stats struct {
	mu          sync.Mutex
	cycles      map[string]float64 // by outcome: ok, skipped, error
	transitions map[string]float64 // by mode/to/result
	checks      map[string]float64 // shadow verdicts
	lastCycle   time.Time
	lastTotal   float64
}

// NewStats returns empty counters.
func NewStats() *Stats {
	return &Stats{cycles: map[string]float64{}, transitions: map[string]float64{}, checks: map[string]float64{}}
}

func (s *Stats) record(rep *CycleReport) {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch {
	case rep.Err != "":
		s.cycles["error"]++
	case rep.Skipped != "":
		s.cycles["skipped"]++
	default:
		s.cycles["ok"]++
	}
	for _, o := range rep.Outcomes {
		s.transitions[string(rep.Mode)+"\xff"+o.To+"\xff"+o.Result]++
	}
	for _, c := range rep.Checks {
		s.checks[c.Verdict]++
	}
	s.lastCycle = rep.StartedAt
	s.lastTotal = float64(rep.Total)
}

// Collectors exposes the counters through the KAS metrics registry.
func (s *Stats) Collectors() []metrics.Collector {
	snap := func(m map[string]float64, split int) []metrics.Sample {
		s.mu.Lock()
		defer s.mu.Unlock()
		out := make([]metrics.Sample, 0, len(m))
		for k, v := range m {
			out = append(out, metrics.Sample{LabelValues: splitKey(k, split), Value: v})
		}
		return out
	}
	return []metrics.Collector{
		metrics.NewCounterFunc("kas_event_reaper_cycles_total", "Event reaper cycles by outcome.", []string{"outcome"},
			func() []metrics.Sample { return snap(s.cycles, 1) }),
		metrics.NewCounterFunc("kas_event_reaper_events_total", "Events handled by mode, target status and result.", []string{"mode", "to", "result"},
			func() []metrics.Sample { return snap(s.transitions, 3) }),
		metrics.NewCounterFunc("kas_event_reaper_shadow_checks_total", "Shadow predictions compared with the database later.", []string{"verdict"},
			func() []metrics.Sample { return snap(s.checks, 1) }),
		metrics.NewGaugeFunc("kas_event_reaper_last_cycle_candidates", "Candidates found in the last cycle.", nil,
			func() []metrics.Sample {
				s.mu.Lock()
				defer s.mu.Unlock()
				return []metrics.Sample{{Value: s.lastTotal}}
			}),
		metrics.NewGaugeFunc("kas_event_reaper_last_cycle_timestamp_seconds", "Start time of the last cycle.", nil,
			func() []metrics.Sample {
				s.mu.Lock()
				defer s.mu.Unlock()
				if s.lastCycle.IsZero() {
					return nil
				}
				return []metrics.Sample{{Value: float64(s.lastCycle.UnixNano()) / 1e9}}
			}),
	}
}

func splitKey(k string, n int) []string {
	out := make([]string, 0, n)
	start := 0
	for i := 0; i < len(k); i++ {
		if k[i] == '\xff' {
			out = append(out, k[start:i])
			start = i + 1
		}
	}
	return append(out, k[start:])
}
