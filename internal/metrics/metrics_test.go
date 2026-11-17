package metrics

import (
	"fmt"
	"strings"
	"sync"
	"testing"
)

func render(t *testing.T, r *Registry) string {
	t.Helper()
	var b strings.Builder
	if _, err := r.WriteTo(&b); err != nil {
		t.Fatal(err)
	}
	return b.String()
}

func TestCounterVecRendersSortedLabelledSeries(t *testing.T) {
	r := NewRegistry()
	c := NewCounterVec("kas_requests_total", "Requests by route and status.", "route", "status")
	r.Register(c)
	c.Inc("/b", "200")
	c.Inc("/a", "500")
	c.Inc("/a", "200")
	c.Inc("/a", "200")
	c.Inc("/a") // wrong number of values: ignored
	want := `# HELP kas_requests_total Requests by route and status.
# TYPE kas_requests_total counter
kas_requests_total{route="/a",status="200"} 2
kas_requests_total{route="/a",status="500"} 1
kas_requests_total{route="/b",status="200"} 1
`
	if got := render(t, r); got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestHistogramIsCumulativeWithInfSumAndCount(t *testing.T) {
	r := NewRegistry()
	h := NewHistogramVec("kas_latency_seconds", "Latency.", []float64{0.1, 1}, "route")
	r.Register(h)
	h.Observe(0.05, "/a")
	h.Observe(0.1, "/a") // on the boundary: le is inclusive
	h.Observe(0.5, "/a")
	h.Observe(7, "/a")
	want := `# HELP kas_latency_seconds Latency.
# TYPE kas_latency_seconds histogram
kas_latency_seconds_bucket{route="/a",le="0.1"} 2
kas_latency_seconds_bucket{route="/a",le="1"} 3
kas_latency_seconds_bucket{route="/a",le="+Inf"} 4
kas_latency_seconds_sum{route="/a"} 7.65
kas_latency_seconds_count{route="/a"} 4
`
	if got := render(t, r); got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestFuncCollectorsAndLabelEscaping(t *testing.T) {
	r := NewRegistry()
	r.Register(NewGaugeFunc("kas_state", "A \\ b\nc", []string{"state"}, func() []Sample {
		return []Sample{{[]string{`we"ird\value` + "\n"}, 3}, {[]string{"x", "extra"}, 9}} // second has the wrong arity
	}))
	r.Register(NewCounterFunc("kas_uses_total", "Uses.", nil, func() []Sample { return []Sample{{nil, 12}} }))
	got := render(t, r)
	for _, want := range []string{
		`# HELP kas_state A \\ b\nc`,
		`# TYPE kas_state gauge`,
		`kas_state{state="we\"ird\\value\n"} 3`,
		`# TYPE kas_uses_total counter`,
		"kas_uses_total 12\n",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q in:\n%s", want, got)
		}
	}
	if strings.Contains(got, "extra") {
		t.Fatal("a sample with the wrong number of label values must be skipped")
	}
}

func TestSeriesAreCappedAgainstCardinalityGrowth(t *testing.T) {
	c := NewCounterVec("kas_capped_total", "Capped.", "id")
	for i := 0; i < MaxSeries+200; i++ {
		c.Inc(fmt.Sprint(i))
	}
	r := NewRegistry()
	r.Register(c)
	if lines := strings.Count(render(t, r), "kas_capped_total{"); lines != MaxSeries {
		t.Fatalf("series = %d, want the cap of %d", lines, MaxSeries)
	}
}

func TestConcurrentUpdatesAreSafe(t *testing.T) {
	c := NewCounterVec("kas_conc_total", "x", "k")
	h := NewHistogramVec("kas_conc_seconds", "x", DefaultBuckets, "k")
	r := NewRegistry()
	r.Register(c)
	r.Register(h)
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 200; j++ {
				c.Inc("a")
				h.Observe(0.01, "a")
				_ = render(t, r)
			}
		}()
	}
	wg.Wait()
	if got := render(t, r); !strings.Contains(got, `kas_conc_total{k="a"} 3200`) || !strings.Contains(got, `kas_conc_seconds_count{k="a"} 3200`) {
		t.Fatalf("lost updates:\n%s", got)
	}
}

func TestInvalidNamesPanicAtConstruction(t *testing.T) {
	for name, build := range map[string]func(){
		"metric name":    func() { NewCounterVec("bad name", "x") },
		"label name":     func() { NewCounterVec("ok_total", "x", "bad-label") },
		"reserved label": func() { NewCounterVec("ok_total", "x", "__name") },
		"le label":       func() { NewHistogramVec("ok_seconds", "x", DefaultBuckets, "le") },
	} {
		t.Run(name, func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Fatal("expected a panic")
				}
			}()
			build()
		})
	}
}
