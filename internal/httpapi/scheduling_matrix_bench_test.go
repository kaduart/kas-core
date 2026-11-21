package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"os"
	"runtime"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"kas-core-pilot/internal/configstore"
	"kas-core-pilot/internal/pgtest"
	"kas-core-pilot/internal/scheduling"
)

// Pool × limit × concurrency matrix for the Agenda read path, measured for a
// fixed time per cell (not a fixed request budget: with a budget, refused
// requests return in microseconds and eat it, which inflates the refusal rate).
//
// Two client behaviours per cell:
//   - "imediato": on 503 the client sends the next request at once (closed
//     loop, worst case, what the phase 2 benchmark did);
//   - "backoff": on 503 the client waits 50 ms × attempt and retries the same
//     request up to 5 times; the latency counted is what the user waits.
//
// KAS_BENCH=1 KAS_BENCH_MATRIX=1 [KAS_BENCH_OUT=file.json] [KAS_BENCH_PG_CONTAINER=pg-sched]

type matrixCell struct {
	Pool        int     `json:"pool_max_conns"`
	Limit       int     `json:"server_limit"`
	Concurrency int     `json:"concurrency"`
	Client      string  `json:"client"`
	Seconds     float64 `json:"seconds"`
	OK          int     `json:"ok"`
	Refused503  int     `json:"refused_503_responses"`
	Failed      int     `json:"failed_after_retries"`
	OtherErrors int     `json:"other_errors"`
	OKPerSecond float64 `json:"ok_per_second"`
	RefusalRate float64 `json:"refusal_rate"`
	UserFailure float64 `json:"user_failure_rate"`
	P50ms       float64 `json:"p50_ms"`
	P95ms       float64 `json:"p95_ms"`
	P99ms       float64 `json:"p99_ms"`
	GoHeapMB    float64 `json:"go_heap_inuse_mb"`
	GoSysMB     float64 `json:"go_sys_mb"`
	PgMemMB     float64 `json:"postgres_mem_mb,omitempty"`
}

func runTimed(pool, limit, c int, client string, d time.Duration, pgContainer string, do func(*rand.Rand) (int, error)) matrixCell {
	runtime.GC()
	var mu sync.Mutex
	var lat []time.Duration
	var refused, failed, other atomic.Int64
	deadline := time.Now().Add(d)
	var wg sync.WaitGroup
	start := time.Now()
	for w := 0; w < c; w++ {
		wg.Add(1)
		go func(seed int64) {
			defer wg.Done()
			rng := rand.New(rand.NewSource(seed))
			for time.Now().Before(deadline) {
				t0 := time.Now()
				ok := false
				for attempt := 1; ; attempt++ {
					code, err := do(rng)
					if err == nil && code < 300 {
						ok = true
						break
					}
					if code != http.StatusServiceUnavailable {
						other.Add(1)
						break
					}
					refused.Add(1)
					if client == "imediato" || attempt == 5 {
						if client == "backoff" {
							failed.Add(1)
						}
						break
					}
					time.Sleep(time.Duration(attempt) * 50 * time.Millisecond)
				}
				if ok {
					el := time.Since(t0)
					mu.Lock()
					lat = append(lat, el)
					mu.Unlock()
				}
			}
		}(int64(w + 1))
	}
	wg.Wait()
	el := time.Since(start).Seconds()
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	_, pgMem := pgStat(pgContainer)
	sort.Slice(lat, func(i, j int) bool { return lat[i] < lat[j] })
	cell := matrixCell{Pool: pool, Limit: limit, Concurrency: c, Client: client, Seconds: el, OK: len(lat),
		Refused503: int(refused.Load()), Failed: int(failed.Load()), OtherErrors: int(other.Load()),
		OKPerSecond: float64(len(lat)) / el, P50ms: pct(lat, .50), P95ms: pct(lat, .95), P99ms: pct(lat, .99),
		GoHeapMB: float64(ms.HeapInuse) / (1 << 20), GoSysMB: float64(ms.Sys) / (1 << 20), PgMemMB: pgMem}
	if total := cell.OK + cell.Refused503; total > 0 {
		cell.RefusalRate = float64(cell.Refused503) / float64(total)
	}
	if client == "imediato" {
		if total := cell.OK + cell.Refused503; total > 0 {
			cell.UserFailure = float64(cell.Refused503) / float64(total)
		}
	} else if total := cell.OK + cell.Failed; total > 0 {
		cell.UserFailure = float64(cell.Failed) / float64(total)
	}
	return cell
}

func TestSchedulingPoolMatrix(t *testing.T) {
	if os.Getenv("KAS_BENCH") != "1" || os.Getenv("KAS_BENCH_MATRIX") != "1" {
		t.Skip("set KAS_BENCH=1 KAS_BENCH_MATRIX=1 to run the pool × limit matrix")
	}
	pgContainer := os.Getenv("KAS_BENCH_PG_CONTAINER")
	baseDSN := pgtest.SchemaDSN(t)
	cfg, err := configstore.Load(strings.NewReader(benchConfig), func(string) (string, bool) { return handlerTokenA, true })
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Timeout: 30 * time.Second, Transport: &http.Transport{MaxIdleConnsPerHost: 300, MaxConnsPerHost: 300}}
	var cells []matrixCell
	seeded := false
	var env benchEnv
	for _, pool := range []int{4, 8, 16, 32} {
		sep := "?"
		if strings.Contains(baseDSN, "?") {
			sep = "&"
		}
		ss, err := scheduling.Open(context.Background(), fmt.Sprintf("%s%spool_max_conns=%d", baseDSN, sep, pool), !seeded)
		if err != nil {
			t.Fatal(err)
		}
		if !seeded {
			env = seedBench(t, ss)
			seeded = true
		} else {
			env.store = ss
		}
		availability := func(base string) func(*rand.Rand) (int, error) {
			return func(rng *rand.Rand) (int, error) {
				d := rng.Intn(len(env.days) - 5)
				req, _ := http.NewRequest(http.MethodGet, base+fmt.Sprintf("/internal/v1/scheduling/availability?tenant_id=%s&resource_id=%s&duration_min=40&from=%s&to=%s",
					benchTenant, env.resources[rng.Intn(len(env.resources))], env.days[d], env.days[d+4]), nil)
				req.Header.Set("X-KAS-Service", "bench-sis")
				req.Header.Set("Authorization", "Bearer "+handlerTokenA)
				resp, err := client.Do(req)
				if err != nil {
					return 0, err
				}
				_, _ = io.Copy(io.Discard, resp.Body)
				resp.Body.Close()
				return resp.StatusCode, nil
			}
		}
		for _, limit := range []int{32, 64, 128} {
			h := NewHandler(cfg, limit, slog.New(slog.NewTextHandler(io.Discard, nil)))
			h.EnableScheduling(ss)
			srv := httptest.NewServer(h)
			for i := 0; i < 30; i++ {
				_, _ = availability(srv.URL)(rand.New(rand.NewSource(int64(i))))
			}
			for _, c := range []int{10, 25, 50, 100} {
				for _, mode := range []string{"imediato", "backoff"} {
					cell := runTimed(pool, limit, c, mode, 3*time.Second, pgContainer, availability(srv.URL))
					cells = append(cells, cell)
					t.Logf("pool=%d limit=%d c=%d %s ok/s=%.0f p99=%.1fms recusa=%.1f%% falha_usuario=%.1f%%",
						pool, limit, c, mode, cell.OKPerSecond, cell.P99ms, 100*cell.RefusalRate, 100*cell.UserFailure)
				}
			}
			srv.Close()
		}
		ss.Close()
	}
	if out := os.Getenv("KAS_BENCH_OUT"); out != "" {
		b, _ := json.MarshalIndent(cells, "", "  ")
		if err := os.WriteFile(out, b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
}
