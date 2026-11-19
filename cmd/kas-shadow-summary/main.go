// Command kas-shadow-summary reads the JSON Lines report written by
// kas-event-reaper in shadow mode (one or more runs) and, optionally, the CRM
// health probe log, and evaluates the objective criteria of the five-day
// shadow comparison (docs/event-reaper/SOMBRA.md §5). It prints Markdown
// (counts only, never event identifiers, safe for a public CI summary) and can
// write the full result as JSON.
//
// Exit codes: 0 all criteria pass; 1 at least one criterion fails;
// 3 nothing failed but the period is shorter than -min-days; 2 usage error.
package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"math"
	"os"
	"sort"
	"strings"
	"time"
)

func main() { os.Exit(run(os.Args[1:], os.Stdout, os.Stderr)) }

type proof struct {
	Users   []string `json:"users"`
	Roles   []string `json:"roles"`
	Actions []string `json:"actions"`
}

type line struct {
	Kind string    `json:"kind"`
	At   time.Time `json:"at"`
	// start
	Mode             string `json:"mode"`
	Protocol         string `json:"protocol"`
	HostClass        string `json:"host_class"`
	Interval         string `json:"interval"`
	Batch            int    `json:"batch"`
	ReadOnlyRequired bool   `json:"read_only_required"`
	ReadOnly         *proof `json:"read_only"`
	// cycle
	StartedAt time.Time     `json:"started_at"`
	Duration  time.Duration `json:"duration_ns"`
	Total     int           `json:"total"`
	Skipped   string        `json:"skipped"`
	Err       string        `json:"error"`
	Outcomes  []struct {
		Result string `json:"result"`
	} `json:"outcomes"`
	Checks []struct {
		EventID   string `json:"event_id"`
		Predicted string `json:"predicted"`
		Observed  string `json:"observed"`
		Verdict   string `json:"verdict"`
		Reason    string `json:"observed_reason"`
	} `json:"shadow_checks"`
	Proc struct {
		RSS     uint64 `json:"rss_bytes"`
		Sys     uint64 `json:"sys_bytes"`
		CPUUser int64  `json:"cpu_user_ms"`
		CPUSys  int64  `json:"cpu_sys_ms"`
	} `json:"proc"`
}

type health struct {
	At     time.Time `json:"at"`
	Status int       `json:"status"`
	Ms     float64   `json:"ms"`
}

// Divergence classes. Only decision_mismatch and unclassified are failures by
// themselves; the others are explained (see SOMBRA.md §5.2).
const (
	clsConcludedByWorker = "concluded_by_worker"      // DV-1: a worker finished the event between prediction and check
	clsSameResultOther   = "same_result_other_writer" // same final status, written by someone else (e.g. the worker failed it)
	clsChangedByOther    = "changed_by_other_writer"  // another writer (worker, admin retry) moved it elsewhere
	clsDecisionMismatch  = "decision_mismatch"        // the Node reaper decided differently: must be 0
	clsUnclassified      = "unclassified"
	clsNodeNotActing     = "node_not_acting" // still processing for many consecutive checks
	clsGone              = "gone"
)

var reaperTargets = map[string]bool{"pending": true, "failed": true, "dead_letter": true}

type criterion struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Pass   bool   `json:"pass"`
	Detail string `json:"detail"`
}

type result struct {
	From, To       time.Time `json:"-"`
	FromS          string    `json:"from"`
	ToS            string    `json:"to"`
	Days           float64   `json:"days"`
	Runs           int       `json:"runs"`
	RunsReadOnly   int       `json:"runs_read_only_proven"`
	RunsNotShadow  int       `json:"runs_not_shadow"`
	Cycles         int       `json:"cycles"`
	ExpectedCycles int       `json:"expected_cycles"`
	// CoveredSlots: 5-minute slots with at least one cycle that finished without error or skip.
	// Duplicated cycles (overlapping runs) count once; error and skipped cycles do not count.
	CoveredSlots   int                 `json:"covered_slots"`
	DuplicateSlots int                 `json:"duplicate_slots"`
	Coverage       float64             `json:"coverage"`
	LongestGap     string              `json:"longest_gap"`
	ErrorCycles    int                 `json:"error_cycles"`
	MaxErrorStreak int                 `json:"max_consecutive_error_cycles"`
	Skipped        map[string]int      `json:"skipped"`
	Candidates     int                 `json:"candidates_total"`
	MaxCandidates  int                 `json:"max_candidates_per_cycle"`
	WriteOutcomes  int                 `json:"write_outcomes"`
	DurP50, DurP95 float64             `json:"-"`
	DurMax         float64             `json:"-"`
	Duration       map[string]float64  `json:"cycle_duration_ms"`
	MaxRSSMB       float64             `json:"max_rss_mb"`
	CPUSeconds     float64             `json:"cpu_seconds"`
	Verdicts       map[string]int      `json:"verdicts"`
	Classes        map[string]int      `json:"divergence_classes"`
	Health         map[string]float64  `json:"crm_health,omitempty"`
	Criteria       []criterion         `json:"criteria"`
	Examples       map[string][]string `json:"examples,omitempty"`
}

type thresholds struct {
	minDays, minCoverage, maxErrorRate, maxP95Ms, maxRSSMB, minHealthOK float64
	maxErrorStreak, stillChecks                                         int
}

func run(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("kas-shadow-summary", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var th thresholds
	var healthPaths, jsonOut string
	var withIDs bool
	fs.Float64Var(&th.minDays, "min-days", 5, "minimum observed period in days (0 for a daily partial summary)")
	fs.Float64Var(&th.minCoverage, "min-coverage", 0.90, "cycles observed / cycles expected for the period")
	fs.Float64Var(&th.maxErrorRate, "max-error-rate", 0.01, "cycles with error / cycles")
	fs.IntVar(&th.maxErrorStreak, "max-error-streak", 3, "consecutive cycles with error")
	fs.Float64Var(&th.maxP95Ms, "max-p95-ms", 2000, "p95 of the cycle duration (two indexed reads per cycle)")
	fs.Float64Var(&th.maxRSSMB, "max-rss-mb", 128, "maximum resident memory of the shadow process")
	fs.Float64Var(&th.minHealthOK, "min-health-ok", 0.99, "CRM /health answers 200 at least this share of the probes")
	fs.IntVar(&th.stillChecks, "still-checks", 4, "consecutive still_processing checks that make an event node_not_acting")
	fs.StringVar(&healthPaths, "health", "", "comma-separated CRM health probe files (JSON Lines: at, status, ms)")
	fs.StringVar(&jsonOut, "json-out", "", "write the full result as JSON to this file")
	fs.BoolVar(&withIDs, "with-ids", false, "include up to 5 event ids per divergence class in the JSON (never in the Markdown)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() == 0 {
		fmt.Fprintln(stderr, "usage: kas-shadow-summary [flags] report.jsonl [more.jsonl...]")
		return 2
	}
	var lines []line
	for _, p := range fs.Args() {
		ls, err := readLines(p)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 2
		}
		lines = append(lines, ls...)
	}
	var hs []health
	if healthPaths != "" {
		for _, p := range strings.Split(healthPaths, ",") {
			h, err := readHealth(strings.TrimSpace(p))
			if err != nil {
				fmt.Fprintln(stderr, err)
				return 2
			}
			hs = append(hs, h...)
		}
	}
	res := evaluate(lines, hs, th, withIDs)
	writeMarkdown(stdout, res)
	if jsonOut != "" {
		b, _ := json.MarshalIndent(res, "", "  ")
		if err := os.WriteFile(jsonOut, b, 0o600); err != nil {
			fmt.Fprintln(stderr, err)
			return 2
		}
	}
	failed, short := false, false
	for _, c := range res.Criteria {
		if !c.Pass {
			if c.ID == "C7" && res.Coverage >= th.minCoverage {
				short = true
				continue
			}
			failed = true
		}
	}
	switch {
	case failed:
		return 1
	case short:
		return 3
	}
	return 0
}

func readLines(path string) ([]line, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []line
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 64<<20)
	n := 0
	for sc.Scan() {
		n++
		b := sc.Bytes()
		if len(strings.TrimSpace(string(b))) == 0 {
			continue
		}
		var l line
		if err := json.Unmarshal(b, &l); err != nil {
			return nil, fmt.Errorf("%s:%d: %w", path, n, err)
		}
		if l.Kind == "" {
			l.Kind = "cycle" // reports written before the start/stop header lines
		}
		out = append(out, l)
	}
	return out, sc.Err()
}

func readHealth(path string) ([]health, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []health
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if strings.TrimSpace(sc.Text()) == "" {
			continue
		}
		var h health
		if err := json.Unmarshal(sc.Bytes(), &h); err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		out = append(out, h)
	}
	return out, sc.Err()
}

func percentile(v []float64, p float64) float64 {
	if len(v) == 0 {
		return 0
	}
	s := append([]float64(nil), v...)
	sort.Float64s(s)
	i := int(math.Ceil(p*float64(len(s)))) - 1
	if i < 0 {
		i = 0
	}
	return s[i]
}

func classify(predicted, observed, reason string) string {
	switch {
	case observed == predicted && reason != "reaper":
		return clsSameResultOther
	case reaperTargets[observed] && reason == "reaper":
		return clsDecisionMismatch
	case reaperTargets[observed]:
		return clsChangedByOther
	case observed != "" && observed != "processing":
		return clsConcludedByWorker
	}
	return clsUnclassified
}

func evaluate(lines []line, hs []health, th thresholds, withIDs bool) result {
	res := result{Skipped: map[string]int{}, Verdicts: map[string]int{}, Classes: map[string]int{}, Examples: map[string][]string{}}
	interval := 5 * time.Minute
	var durs []float64
	var cycles []line
	stillStreak := map[string]int{}
	flagged := map[string]bool{}
	var cpuPerRun []int64
	var lastCPU int64
	example := func(cls, id string) {
		if withIDs && len(res.Examples[cls]) < 5 {
			res.Examples[cls] = append(res.Examples[cls], id)
		}
	}
	for _, l := range lines {
		switch l.Kind {
		case "start":
			res.Runs++
			if l.Mode != "shadow" {
				res.RunsNotShadow++
			}
			if l.ReadOnly != nil && len(l.ReadOnly.Users) > 0 {
				res.RunsReadOnly++
			}
			if d, err := time.ParseDuration(l.Interval); err == nil && d > 0 {
				interval = d
			}
			if lastCPU > 0 {
				cpuPerRun = append(cpuPerRun, lastCPU)
			}
			lastCPU = 0
		case "cycle":
			cycles = append(cycles, l)
			if t := l.Proc.CPUUser + l.Proc.CPUSys; t > lastCPU {
				lastCPU = t // cumulative per process: keep the last (largest) value of each run
			}
		}
	}
	if lastCPU > 0 {
		cpuPerRun = append(cpuPerRun, lastCPU)
	}
	sort.SliceStable(cycles, func(i, j int) bool { return cycles[i].StartedAt.Before(cycles[j].StartedAt) })
	streak := 0
	covered := map[time.Time]int{}
	var prev time.Time
	var longest time.Duration
	for _, c := range cycles {
		res.Cycles++
		if !prev.IsZero() {
			if g := c.StartedAt.Sub(prev); g > longest {
				longest = g
			}
		}
		prev = c.StartedAt
		if res.From.IsZero() {
			res.From = c.StartedAt
		}
		res.To = c.StartedAt
		if c.Err != "" {
			res.ErrorCycles++
			streak++
			if streak > res.MaxErrorStreak {
				res.MaxErrorStreak = streak
			}
		} else {
			streak = 0
		}
		if c.Skipped != "" {
			res.Skipped[c.Skipped]++
		}
		if c.Err == "" && c.Skipped == "" {
			covered[c.StartedAt.UTC().Truncate(interval)]++
		}
		res.Candidates += c.Total
		if c.Total > res.MaxCandidates {
			res.MaxCandidates = c.Total
		}
		for _, o := range c.Outcomes {
			if o.Result != "predicted" && o.Result != "error" {
				res.WriteOutcomes++
			}
		}
		durs = append(durs, float64(c.Duration)/float64(time.Millisecond))
		if mb := float64(c.Proc.RSS) / (1 << 20); mb > res.MaxRSSMB {
			res.MaxRSSMB = mb
		}
		for _, k := range c.Checks {
			res.Verdicts[k.Verdict]++
			switch k.Verdict {
			case "still_processing":
				stillStreak[k.EventID]++
				if stillStreak[k.EventID] >= th.stillChecks && !flagged[k.EventID] {
					flagged[k.EventID] = true
					res.Classes[clsNodeNotActing]++
					example(clsNodeNotActing, k.EventID)
				}
			case "agree":
				delete(stillStreak, k.EventID)
			case "gone":
				delete(stillStreak, k.EventID)
				res.Classes[clsGone]++
			case "diverge":
				delete(stillStreak, k.EventID)
				cls := classify(k.Predicted, k.Observed, k.Reason)
				res.Classes[cls]++
				example(cls, k.EventID)
			}
		}
	}
	for _, c := range cpuPerRun {
		res.CPUSeconds += float64(c) / 1000
	}
	if res.Cycles > 0 {
		span := res.To.Sub(res.From)
		res.Days = span.Hours() / 24
		first, last := res.From.UTC().Truncate(interval), res.To.UTC().Truncate(interval)
		res.ExpectedCycles = int(last.Sub(first)/interval) + 1
		res.CoveredSlots = len(covered)
		for _, n := range covered {
			if n > 1 {
				res.DuplicateSlots++
			}
		}
		res.Coverage = float64(res.CoveredSlots) / float64(res.ExpectedCycles)
		res.FromS, res.ToS = res.From.Format(time.RFC3339), res.To.Format(time.RFC3339)
	}
	res.LongestGap = longest.Round(time.Second).String()
	res.DurP50, res.DurP95 = percentile(durs, 0.5), percentile(durs, 0.95)
	for _, d := range durs {
		res.DurMax = math.Max(res.DurMax, d)
	}
	res.Duration = map[string]float64{"p50": round1(res.DurP50), "p95": round1(res.DurP95), "max": round1(res.DurMax)}
	res.MaxRSSMB = round1(res.MaxRSSMB)

	var hok, hn float64
	var hms []float64
	for _, h := range hs {
		hn++
		if h.Status == 200 {
			hok++
			hms = append(hms, h.Ms)
		}
	}
	if hn > 0 {
		res.Health = map[string]float64{"probes": hn, "ok_share": round4(hok / hn), "p50_ms": round1(percentile(hms, 0.5)), "p95_ms": round1(percentile(hms, 0.95))}
	}

	errRate := 0.0
	if res.Cycles > 0 {
		errRate = float64(res.ErrorCycles) / float64(res.Cycles)
	}
	add := func(id, name string, pass bool, detail string) {
		res.Criteria = append(res.Criteria, criterion{ID: id, Name: name, Pass: pass, Detail: detail})
	}
	add("C1", "Nenhuma escrita pela sombra",
		res.Runs > 0 && res.RunsNotShadow == 0 && res.RunsReadOnly == res.Runs && res.WriteOutcomes == 0,
		fmt.Sprintf("%d execuções; %d com usuário só leitura comprovado pelo servidor; %d fora do modo sombra; %d resultados de escrita", res.Runs, res.RunsReadOnly, res.RunsNotShadow, res.WriteOutcomes))
	add("C2", "Nenhuma decisão diferente do Node e nada sem classe",
		res.Classes[clsDecisionMismatch] == 0 && res.Classes[clsUnclassified] == 0,
		fmt.Sprintf("decision_mismatch=%d, unclassified=%d", res.Classes[clsDecisionMismatch], res.Classes[clsUnclassified]))
	add("C3", "Nenhum evento parado sem explicação",
		res.Classes[clsNodeNotActing] == 0,
		fmt.Sprintf("node_not_acting=%d (evento em processing por %d conferências seguidas)", res.Classes[clsNodeNotActing], th.stillChecks))
	add("C4", "Erros da sombra registrados e dentro do limite",
		res.Cycles > 0 && errRate <= th.maxErrorRate && res.MaxErrorStreak <= th.maxErrorStreak,
		fmt.Sprintf("%d de %d ciclos com erro (%.2f%%; limite %.2f%%); maior sequência %d (limite %d)", res.ErrorCycles, res.Cycles, errRate*100, th.maxErrorRate*100, res.MaxErrorStreak, th.maxErrorStreak))
	healthPass, healthDetail := true, "sem sonda do /health"
	if res.Health != nil {
		healthPass = res.Health["ok_share"] >= th.minHealthOK
		healthDetail = fmt.Sprintf("/health do CRM: %.2f%% de 200 em %.0f sondas (mínimo %.0f%%), p95 %.0f ms", res.Health["ok_share"]*100, hn, th.minHealthOK*100, res.Health["p95_ms"])
	}
	add("C5", "Sem impacto perceptível no CRM",
		res.DurP95 <= th.maxP95Ms && res.MaxCandidates <= 100 && healthPass,
		fmt.Sprintf("ciclo p95 %.0f ms (limite %.0f), no máximo %d candidatos por ciclo; %s", res.DurP95, th.maxP95Ms, res.MaxCandidates, healthDetail))
	add("C6", "Consumo registrado e dentro do limite",
		res.Cycles > 0 && res.MaxRSSMB <= th.maxRSSMB,
		fmt.Sprintf("memória máxima %.1f MB (limite %.0f); CPU total %.1f s", res.MaxRSSMB, th.maxRSSMB, res.CPUSeconds))
	add("C7", "Período e cobertura",
		res.Days >= th.minDays && res.Coverage >= th.minCoverage,
		fmt.Sprintf("%.2f dias (mínimo %.0f); %d de %d intervalos de ciclo com ciclo concluído sem erro (%.1f%%, mínimo %.0f%%); %d intervalos com ciclo duplicado; maior intervalo sem ciclo %s", res.Days, th.minDays, res.CoveredSlots, res.ExpectedCycles, res.Coverage*100, th.minCoverage*100, res.DuplicateSlots, res.LongestGap))
	if !withIDs {
		res.Examples = nil
	}
	return res
}

func round1(f float64) float64 { return math.Round(f*10) / 10 }
func round4(f float64) float64 { return math.Round(f*10000) / 10000 }

func writeMarkdown(w io.Writer, r result) {
	fmt.Fprintf(w, "## Sombra do reaper KAS: resumo\n\n")
	if r.Cycles == 0 {
		fmt.Fprintf(w, "Nenhum ciclo no relatório.\n\n")
	} else {
		fmt.Fprintf(w, "Período: %s → %s (%.2f dias). Execuções: %d. Ciclos: %d.\n\n", r.FromS, r.ToS, r.Days, r.Runs, r.Cycles)
	}
	fmt.Fprintf(w, "| Critério | Resultado | Detalhe |\n|---|---|---|\n")
	for _, c := range r.Criteria {
		st := "PASSA"
		if !c.Pass {
			st = "FALHA"
		}
		fmt.Fprintf(w, "| %s %s | %s | %s |\n", c.ID, c.Name, st, c.Detail)
	}
	fmt.Fprintf(w, "\n**Conferências:** ")
	writeCounts(w, r.Verdicts)
	fmt.Fprintf(w, "\n\n**Divergências por classe:** ")
	writeCounts(w, r.Classes)
	fmt.Fprintf(w, "\n\n**Ciclo (ms):** p50 %.0f, p95 %.0f, máx %.0f. **Pulados:** ", r.Duration["p50"], r.Duration["p95"], r.Duration["max"])
	writeCounts(w, r.Skipped)
	fmt.Fprintln(w)
}

func writeCounts(w io.Writer, m map[string]int) {
	if len(m) == 0 {
		fmt.Fprint(w, "nenhuma")
		return
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s=%d", k, m[k]))
	}
	fmt.Fprint(w, strings.Join(parts, ", "))
}
