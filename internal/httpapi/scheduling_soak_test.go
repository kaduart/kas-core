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
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"kas-core-pilot/internal/configstore"
	"kas-core-pilot/internal/pgtest"
	"kas-core-pilot/internal/scheduling"
)

// Saturation and stability of the Agenda read path with the configuration
// proposed for production (pool 16, -max-concurrent 128), local and fictitious.
// Phases: steady load, at the limit, above the limit (expected 503s), then
// recovery. Each phase is cut in windows so a drift (latency, memory,
// goroutines, Postgres connections) shows up over time.
//
// KAS_BENCH=1 KAS_BENCH_SOAK=1 [KAS_BENCH_SOAK_SCALE=1] [KAS_BENCH_OUT=file.json] [KAS_BENCH_PG_CONTAINER=pg-sched]

type soakWindow struct {
	Phase      string  `json:"phase"`
	Window     int     `json:"window"`
	Clients    int     `json:"clients"`
	Cell       matrixCell
	Goroutines int `json:"goroutines_after"`
	PgConns    int `json:"postgres_connections"`
}

func pgConns(container string) int {
	if container == "" {
		return -1
	}
	out, err := exec.Command("docker", "exec", container, "psql", "-U", "kas", "-d", "kas_core_test", "-tAc",
		"SELECT count(*) FROM pg_stat_activity WHERE backend_type = 'client backend'").Output()
	if err != nil {
		return -1
	}
	n, _ := strconv.Atoi(strings.TrimSpace(string(out)))
	return n
}

func TestSchedulingSoak(t *testing.T) {
	if os.Getenv("KAS_BENCH") != "1" || os.Getenv("KAS_BENCH_SOAK") != "1" {
		t.Skip("set KAS_BENCH=1 KAS_BENCH_SOAK=1 to run the saturation/stability test")
	}
	scale := 1.0
	if v, err := strconv.ParseFloat(os.Getenv("KAS_BENCH_SOAK_SCALE"), 64); err == nil && v > 0 {
		scale = v
	}
	const pool, limit = 16, 128
	pgContainer := os.Getenv("KAS_BENCH_PG_CONTAINER")
	baseDSN := pgtest.SchemaDSN(t)
	sep := "?"
	if strings.Contains(baseDSN, "?") {
		sep = "&"
	}
	ss, err := scheduling.Open(context.Background(), fmt.Sprintf("%s%spool_max_conns=%d", baseDSN, sep, pool), true)
	if err != nil {
		t.Fatal(err)
	}
	defer ss.Close()
	env := seedBench(t, ss)
	cfg, err := configstore.Load(strings.NewReader(benchConfig), func(string) (string, bool) { return handlerTokenA, true })
	if err != nil {
		t.Fatal(err)
	}
	h := NewHandler(cfg, limit, slog.New(slog.NewTextHandler(io.Discard, nil)))
	h.EnableScheduling(ss)
	srv := httptest.NewServer(h)
	defer srv.Close()
	client := &http.Client{Timeout: 30 * time.Second, Transport: &http.Transport{MaxIdleConnsPerHost: 400, MaxConnsPerHost: 400}}
	do := func(rng *rand.Rand) (int, error) {
		d := rng.Intn(len(env.days) - 5)
		req, _ := http.NewRequest(http.MethodGet, srv.URL+fmt.Sprintf("/internal/v1/scheduling/availability?tenant_id=%s&resource_id=%s&duration_min=40&from=%s&to=%s",
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
	for i := 0; i < 50; i++ {
		_, _ = do(rand.New(rand.NewSource(int64(i))))
	}
	phases := []struct {
		name    string
		clients int
		windows int
	}{
		{"estavel_50", 50, 12},
		{"no_limite_128", 128, 6},
		{"acima_do_limite_200", 200, 3},
		{"recuperacao_50", 50, 6},
	}
	win := time.Duration(float64(10*time.Second) * scale)
	var out []soakWindow
	for _, p := range phases {
		for w := 1; w <= p.windows; w++ {
			cell := runTimed(pool, limit, p.clients, "imediato", win, pgContainer, do)
			sw := soakWindow{Phase: p.name, Window: w, Clients: p.clients, Cell: cell, Goroutines: runtime.NumGoroutine(), PgConns: pgConns(pgContainer)}
			out = append(out, sw)
			t.Logf("%s #%d c=%d ok/s=%.0f p50=%.1f p99=%.1fms recusa=%.1f%% outros_erros=%d heap=%.1fMB gor=%d pg_conns=%d pg_mem=%.0fMB",
				p.name, w, p.clients, cell.OKPerSecond, cell.P50ms, cell.P99ms, 100*cell.RefusalRate, cell.OtherErrors, cell.GoHeapMB, sw.Goroutines, sw.PgConns, cell.PgMemMB)
		}
	}
	if path := os.Getenv("KAS_BENCH_OUT"); path != "" {
		b, _ := json.MarshalIndent(out, "", "  ")
		if err := os.WriteFile(path, b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for _, sw := range out {
		if sw.Cell.OtherErrors > 0 {
			t.Errorf("%s #%d: %d errors other than 503", sw.Phase, sw.Window, sw.Cell.OtherErrors)
		}
		if sw.Clients <= limit && sw.Cell.Refused503 > 0 {
			t.Errorf("%s #%d: %d refusals with clients <= limit", sw.Phase, sw.Window, sw.Cell.Refused503)
		}
	}
}
