package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"kas-core-pilot/internal/configstore"
	"kas-core-pilot/internal/pgtest"
	"kas-core-pilot/internal/scheduling"
)

// Agenda load measurement (phase 2). Runs only with KAS_BENCH=1, against the
// real PostgreSQL of KAS_TEST_DATABASE_URL, through a real TCP listener (the
// HTTP client and server share this process). Every number it prints is
// measured here; nothing is compared with the CRM (there is no equivalent
// Node measurement on the same machine and data, so none is claimed).
//
//	KAS_BENCH=1 KAS_BENCH_OUT=resultado.json KAS_BENCH_PG_CONTAINER=pg-sched \
//	  go test ./internal/httpapi -run TestSchedulingLoad -count=1 -timeout 30m -v

const benchTenant = "clinica-aurora"
const benchConfig = `{"contract_version":"v1","config_version":"bench-1",
"tenant_registry":[{"id":"clinica-aurora","status":"active"}],
"key_schema":{"branding.name":{"type":"string"}},"tenants":{},
"modules":{"clinica-aurora":{"scheduling":{"status":"active"}}},
"callers":{"bench-sis":{"token_env":"TOKEN_A","tenants":["clinica-aurora"],"capabilities":["scheduling:read","scheduling:compute"]}}}`

type levelResult struct {
	Scenario    string  `json:"scenario"`
	Limit       int     `json:"server_limit,omitempty"`
	Concurrency int     `json:"concurrency"`
	Requests    int     `json:"requests"`
	OK          int     `json:"ok"`
	Refused503  int     `json:"refused_503"`
	Errors      int     `json:"other_errors"`
	Seconds     float64 `json:"seconds"`
	Throughput  float64 `json:"ok_per_second"`
	P50ms       float64 `json:"p50_ms"`
	P95ms       float64 `json:"p95_ms"`
	P99ms       float64 `json:"p99_ms"`
	MaxMs       float64 `json:"max_ms"`
	GoCPUs      float64 `json:"go_process_cpu_seconds"`
	PgCPUs      float64 `json:"postgres_cpu_seconds,omitempty"`
	GoHeapMB    float64 `json:"go_heap_inuse_mb"`
	GoSysMB     float64 `json:"go_sys_mb"`
	PgMemMB     float64 `json:"postgres_mem_mb,omitempty"`
}

func pct(sorted []time.Duration, p float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	i := int(float64(len(sorted)-1) * p)
	return float64(sorted[i].Microseconds()) / 1000
}

func procCPU() float64 {
	var ru syscall.Rusage
	_ = syscall.Getrusage(syscall.RUSAGE_SELF, &ru)
	tv := func(t syscall.Timeval) float64 { return float64(t.Sec) + float64(t.Usec)/1e6 }
	return tv(ru.Utime) + tv(ru.Stime)
}

// pgStat reads the PostgreSQL container's cgroup: CPU seconds used and memory.
func pgStat(container string) (cpu, memMB float64) {
	if container == "" {
		return 0, 0
	}
	// cgroup v2 first, then v1 (cpuacct in nanoseconds).
	script := `if [ -f /sys/fs/cgroup/cpu.stat ]; then echo v2 $(grep usage_usec /sys/fs/cgroup/cpu.stat | cut -d' ' -f2) $(cat /sys/fs/cgroup/memory.current);
else echo v1 $(cat /sys/fs/cgroup/cpuacct/cpuacct.usage) $(cat /sys/fs/cgroup/memory/memory.usage_in_bytes); fi`
	out, err := exec.Command("docker", "exec", container, "sh", "-c", script).Output()
	if err != nil {
		return 0, 0
	}
	f := strings.Fields(string(out))
	if len(f) < 3 {
		return 0, 0
	}
	u, _ := strconv.ParseFloat(f[1], 64)
	m, _ := strconv.ParseFloat(f[2], 64)
	if f[0] == "v1" {
		return u / 1e9, m / (1 << 20)
	}
	return u / 1e6, m / (1 << 20)
}

type benchEnv struct {
	store     *scheduling.Store
	resources []string
	days      []string
}

func seedBench(t *testing.T, s *scheduling.Store) benchEnv {
	t.Helper()
	ctx := context.Background()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(s.PutSettings(ctx, benchTenant, scheduling.Settings{Zone: "America/Sao_Paulo", Holidays: []string{}, ClientExclusive: true}))
	env := benchEnv{store: s}
	rules := []scheduling.WeeklyRule{}
	for wd := 1; wd <= 5; wd++ {
		rules = append(rules, scheduling.WeeklyRule{Weekday: wd, StartMin: 8 * 60, EndMin: 18 * 60})
	}
	loc, _ := time.LoadLocation("America/Sao_Paulo")
	first := time.Now().In(loc).AddDate(0, 0, 1)
	first = time.Date(first.Year(), first.Month(), first.Day(), 0, 0, 0, 0, loc)
	for d := 0; d < 42; d++ {
		day := first.AddDate(0, 0, d)
		if day.Weekday() != time.Saturday && day.Weekday() != time.Sunday {
			env.days = append(env.days, day.Format("2006-01-02"))
		}
	}
	// 20 professionals; 10 of the 15 slots of each weekday taken (fictitious).
	var items []scheduling.MirrorItem
	for r := 0; r < 20; r++ {
		id := fmt.Sprintf("prof-%02d", r)
		env.resources = append(env.resources, id)
		must(s.PutResource(ctx, benchTenant, scheduling.Resource{ID: id, Kind: "person", Label: id, Capacity: 1, Active: true}))
		must(s.PutWeeklyRules(ctx, benchTenant, id, rules))
		for _, ds := range env.days {
			day, _ := time.ParseInLocation("2006-01-02", ds, loc)
			for k := 0; k < 10; k++ {
				st := day.Add(8*time.Hour + time.Duration(k)*40*time.Minute)
				items = append(items, scheduling.MirrorItem{BookingID: fmt.Sprintf("ag-%s-%s-%d", id, ds, k), ResourceID: id,
					ClientRef: fmt.Sprintf("cl-%d", (r*7+k)%300), Start: st.UTC(), End: st.Add(40 * time.Minute).UTC(),
					Status: scheduling.StatusScheduled, SourceStatus: "scheduled", SourceUpdatedAt: time.Now()})
			}
		}
	}
	for i := 0; i < len(items); i += 2000 {
		end := min(i+2000, len(items))
		if _, err := s.MirrorApply(ctx, benchTenant, "bench", items[i:end], scheduling.MirrorCursor{}, false); err != nil {
			t.Fatal(err)
		}
	}
	t.Logf("dataset: %d professionals, %d working days, %d bookings", len(env.resources), len(env.days), len(items))
	return env
}

// runLevel calls do from c workers until total requests are done.
func runLevel(name string, limit, c, total int, pgContainer string, do func(rng *rand.Rand) (int, error)) levelResult {
	runtime.GC()
	var ms runtime.MemStats
	cpu0 := procCPU()
	pg0, _ := pgStat(pgContainer)
	lat := make([]time.Duration, 0, total)
	var mu sync.Mutex
	var next, refused, other atomic.Int64
	var wg sync.WaitGroup
	start := time.Now()
	for w := 0; w < c; w++ {
		wg.Add(1)
		go func(seed int64) {
			defer wg.Done()
			rng := rand.New(rand.NewSource(seed))
			for next.Add(1) <= int64(total) {
				t0 := time.Now()
				code, err := do(rng)
				d := time.Since(t0)
				switch {
				case err == nil && code < 300:
					mu.Lock()
					lat = append(lat, d)
					mu.Unlock()
				case code == http.StatusServiceUnavailable:
					refused.Add(1)
				default:
					other.Add(1)
				}
			}
		}(int64(w + 1))
	}
	wg.Wait()
	el := time.Since(start).Seconds()
	runtime.ReadMemStats(&ms)
	pg1, pgMem := pgStat(pgContainer)
	sort.Slice(lat, func(i, j int) bool { return lat[i] < lat[j] })
	r := levelResult{Scenario: name, Limit: limit, Concurrency: c, Requests: total, OK: len(lat), Refused503: int(refused.Load()),
		Errors: int(other.Load()), Seconds: el, Throughput: float64(len(lat)) / el, P50ms: pct(lat, 0.50), P95ms: pct(lat, 0.95),
		P99ms: pct(lat, 0.99), GoCPUs: procCPU() - cpu0, GoHeapMB: float64(ms.HeapInuse) / (1 << 20), GoSysMB: float64(ms.Sys) / (1 << 20),
		PgMemMB: pgMem}
	if pgContainer != "" {
		r.PgCPUs = pg1 - pg0
	}
	if len(lat) > 0 {
		r.MaxMs = float64(lat[len(lat)-1].Microseconds()) / 1000
	}
	return r
}

func TestSchedulingLoad(t *testing.T) {
	if os.Getenv("KAS_BENCH") != "1" {
		t.Skip("set KAS_BENCH=1 to run the Agenda load measurement")
	}
	pgContainer := os.Getenv("KAS_BENCH_PG_CONTAINER")
	dsn := pgtest.SchemaDSN(t)
	ctx := context.Background()
	ss, err := scheduling.Open(ctx, dsn, true)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(ss.Close)
	env := seedBench(t, ss)
	cfg, err := configstore.Load(strings.NewReader(benchConfig), func(string) (string, bool) { return handlerTokenA, true })
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Timeout: 30 * time.Second, Transport: &http.Transport{MaxIdleConnsPerHost: 200, MaxConnsPerHost: 200}}
	call := func(base, method, path, body string) (int, error) {
		req, _ := http.NewRequest(method, base+path, strings.NewReader(body))
		req.Header.Set("X-KAS-Service", "bench-sis")
		req.Header.Set("Authorization", "Bearer "+handlerTokenA)
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		resp, err := client.Do(req)
		if err != nil {
			return 0, err
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		return resp.StatusCode, nil
	}
	availability := func(base string) func(*rand.Rand) (int, error) {
		return func(rng *rand.Rand) (int, error) {
			d := rng.Intn(len(env.days) - 5)
			return call(base, http.MethodGet, fmt.Sprintf("/internal/v1/scheduling/availability?tenant_id=%s&resource_id=%s&duration_min=40&from=%s&to=%s",
				benchTenant, env.resources[rng.Intn(len(env.resources))], env.days[d], env.days[d+4]), "")
		}
	}
	conflicts := func(base string) func(*rand.Rand) (int, error) {
		return func(rng *rand.Rand) (int, error) {
			loc, _ := time.LoadLocation("America/Sao_Paulo")
			day, _ := time.ParseInLocation("2006-01-02", env.days[rng.Intn(len(env.days))], loc)
			st := day.Add(8*time.Hour + time.Duration(rng.Intn(15))*40*time.Minute)
			body := fmt.Sprintf(`{"contract_version":"v1","tenant_id":"%s","candidates":[{"key":"c1","resource_id":"%s","client_ref":"cl-%d","starts_at":"%s","ends_at":"%s"}]}`,
				benchTenant, env.resources[rng.Intn(len(env.resources))], rng.Intn(300), st.Format(time.RFC3339), st.Add(40*time.Minute).Format(time.RFC3339))
			return call(base, http.MethodPost, "/internal/v1/scheduling/conflicts:check", body)
		}
	}
	levels := []int{1, 10, 25, 50, 100}
	var results []levelResult
	for _, limit := range []int{32, 128} {
		h := NewHandler(cfg, limit, slog.New(slog.NewTextHandler(io.Discard, nil)))
		h.EnableScheduling(ss)
		srv := httptest.NewServer(h)
		for _, c := range levels {
			for i := 0; i < 20; i++ { // warm-up
				_, _ = availability(srv.URL)(rand.New(rand.NewSource(int64(i))))
			}
			r := runLevel("availability_5_days", limit, c, max(400, 40*c), pgContainer, availability(srv.URL))
			results = append(results, r)
			t.Logf("%+v", r)
			r = runLevel("conflicts_check", limit, c, max(400, 40*c), pgContainer, conflicts(srv.URL))
			results = append(results, r)
			t.Logf("%+v", r)
		}
		srv.Close()
	}
	// Writes (store, no HTTP route yet): distinct free slots, all on one
	// professional (they wait for its lock) or spread over 20.
	loc, _ := time.LoadLocation("America/Sao_Paulo")
	// One-professional runs use prof-15..19 (one per level); spread runs share
	// prof-00..14 with one counter, so no two writes ever aim at the same slot.
	var spreadSeq atomic.Int64
	var firstWriteErr atomic.Pointer[error]
	write := func(spread bool, level int) func(*rand.Rand) (int, error) {
		var seq atomic.Int64
		return func(rng *rand.Rand) (int, error) {
			n := seq.Add(1) - 1
			res := env.resources[15+level]
			if spread {
				n = spreadSeq.Add(1) - 1
				res = env.resources[int(n)%15]
			}
			// Free slots: 14:40-17:20 are never seeded; walk days and the 4 free starts.
			slot := int(n / 15)
			if !spread {
				slot = int(n)
			}
			if slot/4 >= len(env.days) {
				return 500, fmt.Errorf("bench ran out of free slots")
			}
			day, _ := time.ParseInLocation("2006-01-02", env.days[slot/4], loc)
			st := day.Add(14*time.Hour + 40*time.Minute + time.Duration(slot%4)*40*time.Minute)
			_, _, err := ss.CreateBooking(ctx, benchTenant, scheduling.NewBooking{ResourceID: res, ClientRef: fmt.Sprintf("cl-w-%s-%d", res, n),
				IdempotencyKey: fmt.Sprintf("w-%s-%d", res, n), Actor: "bench", Start: st, End: st.Add(40 * time.Minute)})
			if err != nil {
				firstWriteErr.CompareAndSwap(nil, &err)
				return 500, err
			}
			return 201, nil
		}
	}
	for li, c := range levels {
		for _, spread := range []bool{false, true} {
			name := "write_one_professional"
			if spread {
				name = "write_20_professionals"
			}
			total := 100
			if spread {
				total = max(100, 4*c)
			}
			r := runLevel(name, 0, c, total, pgContainer, write(spread, li))
			results = append(results, r)
			t.Logf("%+v", r)
		}
	}
	if e := firstWriteErr.Load(); e != nil {
		t.Errorf("write failed: %v", *e)
	}
	// Failure: PostgreSQL paused for 3 s in the middle of a 50-concurrency read run.
	if pgContainer != "" {
		h := NewHandler(cfg, 128, slog.New(slog.NewTextHandler(io.Discard, nil)))
		h.EnableScheduling(ss)
		srv := httptest.NewServer(h)
		var outcome sync.Map
		go func() {
			time.Sleep(1500 * time.Millisecond)
			_ = exec.Command("docker", "pause", pgContainer).Run()
			outcome.Store("paused_at", time.Now())
			time.Sleep(3 * time.Second)
			_ = exec.Command("docker", "unpause", pgContainer).Run()
			outcome.Store("resumed_at", time.Now())
		}()
		var firstOKAfter atomic.Int64
		base := availability(srv.URL)
		r := runLevel("failure_pg_paused_3s", 128, 50, 6000, "", func(rng *rand.Rand) (int, error) {
			code, err := base(rng)
			if v, ok := outcome.Load("resumed_at"); ok && err == nil && code == 200 {
				firstOKAfter.CompareAndSwap(0, time.Since(v.(time.Time)).Milliseconds()+1)
			}
			return code, err
		})
		srv.Close()
		results = append(results, r)
		t.Logf("%+v first OK %d ms after resume", r, firstOKAfter.Load()-1)
		results = append(results, levelResult{Scenario: fmt.Sprintf("failure_first_ok_after_resume_ms=%d", firstOKAfter.Load()-1)})
	}
	if out := os.Getenv("KAS_BENCH_OUT"); out != "" {
		var b bytes.Buffer
		enc := json.NewEncoder(&b)
		enc.SetIndent("", "  ")
		_ = enc.Encode(results)
		if err := os.WriteFile(out, b.Bytes(), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}
