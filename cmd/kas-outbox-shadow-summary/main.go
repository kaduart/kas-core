// Command kas-outbox-shadow-summary reads the JSON Lines report written by
// kas-outbox-recovery in shadow mode (one or more runs) and, optionally, the
// CRM health probe log. It evaluates the objective criteria of the Outbox
// shadow (docs/outbox-recovery/SOMBRA.md) and tabulates what the recovery
// WOULD do with the failed Outbox entries: by event type, proposed
// destination queue, attempts and age. It prints Markdown with counts, event
// types and queue names only (never entry ids, never payload), safe for a
// public CI summary, and can write the full result as JSON.
//
// -since/-until restrict the evaluation to an official window (cycles whose
// start falls in [since, until)).
//
// Exit codes: 0 all criteria pass; 1 at least one criterion fails;
// 3 nothing failed but the period is shorter than -min-days; 4 nothing failed
// but no failed entry was observed (nothing to evaluate); 2 usage error.
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

	"kas-core-pilot/internal/legacy/crmoutbox"
)

func main() { os.Exit(run(os.Args[1:], os.Stdout, os.Stderr)) }

type proof struct {
	Users   []string `json:"users"`
	Roles   []string `json:"roles"`
	Actions []string `json:"actions"`
}

type item struct {
	ID           string   `json:"id"`
	Type         string   `json:"type"`
	Action       string   `json:"action"`
	Reason       string   `json:"reason"`
	Attempts     int64    `json:"attempts"`
	Recoveries   int64    `json:"recoveries"`
	AgeSeconds   int64    `json:"age_s"`
	Destinations []string `json:"destinations"`
	Known        bool     `json:"known"`
}

type line struct {
	Kind string `json:"kind"`
	// start
	Mode     string `json:"mode"`
	Interval string `json:"interval"`
	ReadOnly *proof `json:"read_only"`
	// cycle
	StartedAt time.Time     `json:"started_at"`
	Duration  time.Duration `json:"duration_ns"`
	Err       string        `json:"error"`
	Scanned   int           `json:"scanned"`
	Recovered int           `json:"recovered"`
	Exhausted int           `json:"exhausted"`
	LostRaces int           `json:"lostRaces"`
	Items     []item        `json:"items"`
	Census    *struct {
		ByStatus        map[string]int64 `json:"byStatus"`
		FailedExhausted int64            `json:"failedExhausted"`
		PendingStale    int64            `json:"pendingStale"`
	} `json:"census"`
}

type health struct {
	At     time.Time `json:"at"`
	Status int       `json:"status"`
	Ms     float64   `json:"ms"`
}

type criterion struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	Pass         bool   `json:"pass"`
	Inconclusive bool   `json:"inconclusive,omitempty"`
	Detail       string `json:"detail"`
}

// typeRow: distinct failed entries of one event type, by what the shadow would do
// (last observation of each entry in the period).
type typeRow struct {
	Type         string   `json:"type"`
	Entries      int      `json:"entries"`
	Recover      int      `json:"would_recover"`
	Exhaust      int      `json:"would_exhaust"`
	Wait         int      `json:"would_wait"` // backoff not elapsed, malformed
	Known        bool     `json:"known"`
	Destinations []string `json:"destinations"`
	External     bool     `json:"external_send"`
}

type result struct {
	WindowFrom     string             `json:"window_from,omitempty"`
	WindowTo       string             `json:"window_to,omitempty"`
	FromS          string             `json:"from"`
	ToS            string             `json:"to"`
	Days           float64            `json:"days"`
	Runs           int                `json:"runs"`
	RunsReadOnly   int                `json:"runs_read_only_proven"`
	RunsNotShadow  int                `json:"runs_not_shadow"`
	Cycles         int                `json:"cycles"`
	ExpectedCycles int                `json:"expected_cycles"`
	CoveredSlots   int                `json:"covered_slots"`
	Coverage       float64            `json:"coverage"`
	ErrorCycles    int                `json:"error_cycles"`
	MaxErrorStreak int                `json:"max_consecutive_error_cycles"`
	Writes         int                `json:"writes"`
	Duration       map[string]float64 `json:"cycle_duration_ms"`
	Health         map[string]float64 `json:"crm_health,omitempty"`
	// Distinct failed entries seen in the period (last observation of each).
	Entries        int            `json:"failed_entries"`
	ByAction       map[string]int `json:"by_action"`
	ByReason       map[string]int `json:"by_reason"`
	Types          []typeRow      `json:"types"`
	RecoverByQueue map[string]int `json:"would_recover_by_queue"`
	// Entries the shadow would recover whose destinations include a queue that
	// sends to a person (WhatsApp/e-mail).
	RecoverExternal int            `json:"would_recover_external_send"`
	RecoverUnknown  int            `json:"would_recover_unknown_type"`
	Attempts        map[string]int `json:"would_recover_by_attempts"`
	Age             map[string]int `json:"would_recover_by_age"`
	AgeExternal     map[string]int `json:"would_recover_external_by_age"`
	// Last census of the period (all statuses, counts only).
	Census       map[string]int64 `json:"last_census_by_status,omitempty"`
	PendingStale int64            `json:"last_census_pending_stale"`
	Criteria     []criterion      `json:"criteria"`
}

type thresholds struct {
	minDays, minCoverage, maxErrorRate, maxP95Ms, minHealthOK float64
	maxErrorStreak                                            int
	since, until                                              time.Time
}

func run(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("kas-outbox-shadow-summary", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var th thresholds
	var healthPaths, jsonOut, since, until string
	fs.Float64Var(&th.minDays, "min-days", 5, "minimum observed period in days (0 for a partial summary)")
	fs.Float64Var(&th.minCoverage, "min-coverage", 0.90, "share of cycle slots with a cycle that finished without error")
	fs.Float64Var(&th.maxErrorRate, "max-error-rate", 0.01, "cycles with error / cycles")
	fs.IntVar(&th.maxErrorStreak, "max-error-streak", 3, "consecutive cycles with error")
	fs.Float64Var(&th.maxP95Ms, "max-p95-ms", 2000, "p95 of the cycle duration")
	fs.Float64Var(&th.minHealthOK, "min-health-ok", 0.99, "CRM /health answers 200 at least this share of the probes")
	fs.StringVar(&healthPaths, "health", "", "comma-separated CRM health probe files (JSON Lines: at, status, ms)")
	fs.StringVar(&jsonOut, "json-out", "", "write the full result as JSON to this file")
	fs.StringVar(&since, "since", "", "official window start, RFC3339; cycles before it are warm-up and ignored")
	fs.StringVar(&until, "until", "", "official window end (exclusive), RFC3339; requires -since")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if since != "" || until != "" {
		var err error
		if since == "" || until == "" {
			fmt.Fprintln(stderr, "-since and -until go together")
			return 2
		}
		if th.since, err = time.Parse(time.RFC3339, since); err != nil {
			fmt.Fprintln(stderr, "-since:", err)
			return 2
		}
		if th.until, err = time.Parse(time.RFC3339, until); err != nil {
			fmt.Fprintln(stderr, "-until:", err)
			return 2
		}
		if !th.until.After(th.since) {
			fmt.Fprintln(stderr, "-until must be after -since")
			return 2
		}
	}
	if fs.NArg() == 0 {
		fmt.Fprintln(stderr, "usage: kas-outbox-shadow-summary [flags] report.jsonl [more.jsonl...]")
		return 2
	}
	var lines []line
	for _, p := range fs.Args() {
		ls, err := readJSONL[line](p)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 2
		}
		lines = append(lines, ls...)
	}
	var hs []health
	if healthPaths != "" {
		for _, p := range strings.Split(healthPaths, ",") {
			h, err := readJSONL[health](strings.TrimSpace(p))
			if err != nil {
				fmt.Fprintln(stderr, err)
				return 2
			}
			hs = append(hs, h...)
		}
	}
	res := evaluate(lines, hs, th)
	writeMarkdown(stdout, res)
	if jsonOut != "" {
		b, _ := json.MarshalIndent(res, "", "  ")
		if err := os.WriteFile(jsonOut, b, 0o600); err != nil {
			fmt.Fprintln(stderr, err)
			return 2
		}
	}
	failed, short, inconclusive := false, false, false
	for _, c := range res.Criteria {
		if c.Inconclusive {
			inconclusive = true
		}
		if !c.Pass {
			if c.ID == "O4" && res.Coverage >= th.minCoverage {
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
	case inconclusive:
		return 4
	}
	return 0
}

func readJSONL[T any](path string) ([]T, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []T
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 64<<20)
	n := 0
	for sc.Scan() {
		n++
		if strings.TrimSpace(sc.Text()) == "" {
			continue
		}
		var v T
		if err := json.Unmarshal(sc.Bytes(), &v); err != nil {
			return nil, fmt.Errorf("%s:%d: %w", path, n, err)
		}
		out = append(out, v)
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

func ageBucket(s int64) string {
	switch {
	case s < 0:
		return "desconhecida"
	case s < 3600:
		return "até 1 h"
	case s < 86400:
		return "1 h a 24 h"
	case s < 7*86400:
		return "1 a 7 dias"
	}
	return "mais de 7 dias"
}

func attemptsBucket(n int64) string {
	if n >= 5 {
		return "5+"
	}
	return fmt.Sprint(n)
}

func evaluate(lines []line, hs []health, th thresholds) result {
	res := result{ByAction: map[string]int{}, ByReason: map[string]int{}, RecoverByQueue: map[string]int{},
		Attempts: map[string]int{}, Age: map[string]int{}, AgeExternal: map[string]int{}}
	windowed := !th.since.IsZero()
	inWindow := func(t time.Time) bool { return !windowed || (!t.Before(th.since) && t.Before(th.until)) }
	interval := 5 * time.Minute
	var cycles []line
	var cur *line
	curIn := false
	closeRun := func() {
		if cur != nil && curIn {
			res.Runs++
			if cur.Mode != "shadow" {
				res.RunsNotShadow++
			}
			if cur.ReadOnly != nil && len(cur.ReadOnly.Users) > 0 {
				res.RunsReadOnly++
			}
		}
	}
	for i := range lines {
		l := lines[i]
		switch l.Kind {
		case "start":
			closeRun()
			cur, curIn = &lines[i], false
			if d, err := time.ParseDuration(l.Interval); err == nil && d > 0 {
				interval = d
			}
		case "cycle":
			if !inWindow(l.StartedAt) {
				continue
			}
			curIn = true
			cycles = append(cycles, l)
		}
	}
	closeRun()
	sort.SliceStable(cycles, func(i, j int) bool { return cycles[i].StartedAt.Before(cycles[j].StartedAt) })

	last := map[string]item{} // entry id -> last observation
	covered := map[time.Time]bool{}
	var durs []float64
	var from, to time.Time
	streak := 0
	for _, c := range cycles {
		res.Cycles++
		if from.IsZero() {
			from = c.StartedAt
		}
		to = c.StartedAt
		res.Writes += c.Recovered + c.Exhausted + c.LostRaces
		durs = append(durs, float64(c.Duration)/float64(time.Millisecond))
		if c.Err != "" {
			res.ErrorCycles++
			streak++
			if streak > res.MaxErrorStreak {
				res.MaxErrorStreak = streak
			}
			continue
		}
		streak = 0
		covered[c.StartedAt.UTC().Truncate(interval)] = true
		for _, it := range c.Items {
			last[it.ID] = it
		}
		if c.Census != nil {
			res.Census = c.Census.ByStatus
			res.PendingStale = c.Census.PendingStale
		}
	}
	if res.Cycles > 0 {
		res.FromS, res.ToS = from.Format(time.RFC3339), to.Format(time.RFC3339)
		res.Days = to.Sub(from).Hours() / 24
		first, lastSlot := from.UTC().Truncate(interval), to.UTC().Truncate(interval)
		res.ExpectedCycles = int(lastSlot.Sub(first)/interval) + 1
		if windowed {
			res.Days = th.until.Sub(th.since).Hours() / 24
			res.ExpectedCycles = int(th.until.Sub(th.since.UTC().Truncate(interval)) / interval)
		}
		res.CoveredSlots = len(covered)
		res.Coverage = float64(res.CoveredSlots) / float64(res.ExpectedCycles)
	}
	if windowed {
		res.WindowFrom, res.WindowTo = th.since.Format(time.RFC3339), th.until.Format(time.RFC3339)
	}
	res.Duration = map[string]float64{"p50": round1(percentile(durs, 0.5)), "p95": round1(percentile(durs, 0.95)), "max": round1(percentile(durs, 1))}

	rows := map[string]*typeRow{}
	for _, it := range last {
		res.Entries++
		res.ByAction[it.Action]++
		res.ByReason[it.Reason]++
		r := rows[it.Type]
		if r == nil {
			q, known := crmoutbox.Destinations(it.Type)
			r = &typeRow{Type: it.Type, Known: known, Destinations: q}
			if r.Destinations == nil {
				r.Destinations = []string{}
			}
			for _, x := range q {
				r.External = r.External || crmoutbox.ExternalSend(x)
			}
			rows[it.Type] = r
		}
		r.Entries++
		switch it.Action {
		case "recover":
			r.Recover++
			res.Attempts[attemptsBucket(it.Attempts)]++
			res.Age[ageBucket(it.AgeSeconds)]++
			if !it.Known {
				res.RecoverUnknown++
			}
			ext := false
			for _, q := range it.Destinations {
				res.RecoverByQueue[q]++
				ext = ext || crmoutbox.ExternalSend(q)
			}
			if ext {
				res.RecoverExternal++
				res.AgeExternal[ageBucket(it.AgeSeconds)]++
			}
		case "exhaust":
			r.Exhaust++
		default:
			r.Wait++
		}
	}
	for _, r := range rows {
		res.Types = append(res.Types, *r)
	}
	sort.Slice(res.Types, func(i, j int) bool {
		if res.Types[i].Entries != res.Types[j].Entries {
			return res.Types[i].Entries > res.Types[j].Entries
		}
		return res.Types[i].Type < res.Types[j].Type
	})

	var hok, hn float64
	var hms []float64
	for _, h := range hs {
		if !inWindow(h.At) {
			continue
		}
		hn++
		if h.Status == 200 {
			hok++
			hms = append(hms, h.Ms)
		}
	}
	if hn > 0 {
		res.Health = map[string]float64{"probes": hn, "ok_share": round4(hok / hn), "p95_ms": round1(percentile(hms, 0.95))}
	}

	add := func(id, name string, pass bool, detail string) {
		res.Criteria = append(res.Criteria, criterion{ID: id, Name: name, Pass: pass, Detail: detail})
	}
	add("O1", "Nenhuma escrita pela sombra",
		res.Runs > 0 && res.RunsNotShadow == 0 && res.RunsReadOnly == res.Runs && res.Writes == 0,
		fmt.Sprintf("%d execuções; %d com usuário só leitura comprovado pelo servidor; %d fora do modo sombra; %d escritas (recuperações, desistências, disputas)", res.Runs, res.RunsReadOnly, res.RunsNotShadow, res.Writes))
	errRate := 0.0
	if res.Cycles > 0 {
		errRate = float64(res.ErrorCycles) / float64(res.Cycles)
	}
	add("O2", "Erros da sombra registrados e dentro do limite",
		res.Cycles > 0 && errRate <= th.maxErrorRate && res.MaxErrorStreak <= th.maxErrorStreak,
		fmt.Sprintf("%d de %d ciclos com erro (%.2f%%; limite %.2f%%); maior sequência %d (limite %d)", res.ErrorCycles, res.Cycles, errRate*100, th.maxErrorRate*100, res.MaxErrorStreak, th.maxErrorStreak))
	healthPass, healthDetail := true, "sem sonda do /health"
	if res.Health != nil {
		healthPass = res.Health["ok_share"] >= th.minHealthOK
		healthDetail = fmt.Sprintf("/health do CRM: %.2f%% de 200 em %.0f sondas (mínimo %.0f%%)", res.Health["ok_share"]*100, hn, th.minHealthOK*100)
	}
	add("O3", "Sem impacto perceptível no CRM",
		res.Duration["p95"] <= th.maxP95Ms && healthPass,
		fmt.Sprintf("ciclo p95 %.0f ms (limite %.0f); %s", res.Duration["p95"], th.maxP95Ms, healthDetail))
	add("O4", "Período e cobertura",
		res.Days >= th.minDays && res.Coverage >= th.minCoverage,
		fmt.Sprintf("%.2f dias (mínimo %.0f); %d de %d intervalos com ciclo concluído sem erro (%.1f%%, mínimo %.0f%%)", res.Days, th.minDays, res.CoveredSlots, res.ExpectedCycles, res.Coverage*100, th.minCoverage*100))
	inconclusive := res.Entries == 0
	detail := fmt.Sprintf("%d entradas failed distintas observadas", res.Entries)
	if inconclusive {
		detail += " — INCONCLUSIVO: nenhuma falha no período, nada para avaliar"
	}
	add("O5", "Há falhas para avaliar", true, detail)
	res.Criteria[len(res.Criteria)-1].Inconclusive = inconclusive
	return res
}

func round1(f float64) float64 { return math.Round(f*10) / 10 }
func round4(f float64) float64 { return math.Round(f*10000) / 10000 }

func writeMarkdown(w io.Writer, r result) {
	fmt.Fprintf(w, "## Sombra da recuperação do Outbox (failed): resumo\n\n")
	fmt.Fprintf(w, "Só observação: nada foi republicado, nenhum estado foi alterado e nenhuma notificação foi enviada.\n\n")
	if r.WindowFrom != "" {
		fmt.Fprintf(w, "Janela oficial: %s → %s (ciclos fora dela são aquecimento e não contam).\n\n", r.WindowFrom, r.WindowTo)
	}
	if r.Cycles == 0 {
		fmt.Fprintf(w, "Nenhum ciclo no relatório.\n\n")
	} else {
		fmt.Fprintf(w, "Período: %s → %s. Execuções: %d. Ciclos: %d. Ciclo p50 %.0f ms, p95 %.0f ms.\n\n", r.FromS, r.ToS, r.Runs, r.Cycles, r.Duration["p50"], r.Duration["p95"])
	}
	fmt.Fprintf(w, "| Critério | Resultado | Detalhe |\n|---|---|---|\n")
	for _, c := range r.Criteria {
		st := "PASSA"
		switch {
		case !c.Pass:
			st = "FALHA"
		case c.Inconclusive:
			st = "INCONCLUSIVO"
		}
		fmt.Fprintf(w, "| %s %s | %s | %s |\n", c.ID, c.Name, st, c.Detail)
	}
	fmt.Fprintf(w, "\n### O que a recuperação faria (entradas vistas como failed no período, última observação de cada)\n\n")
	fmt.Fprintf(w, "Total: %d. Por ação: %s. Por motivo: %s.\n\n", r.Entries, counts(r.ByAction), counts(r.ByReason))
	if len(r.Types) > 0 {
		fmt.Fprintf(w, "| Tipo de evento | Entradas | Recuperaria | Desistiria | Aguardaria | Destino proposto (filas) | Pode enviar a pessoa? |\n|---|---|---|---|---|---|---|\n")
		for _, t := range r.Types {
			dest := strings.Join(t.Destinations, ", ")
			switch {
			case !t.Known:
				dest = "sem rota (UNKNOWN_EVENT_TYPE)"
			case dest == "":
				dest = "nenhuma (marcado como publicado)"
			}
			ext := "não"
			if t.External {
				ext = "sim (WhatsApp/e-mail)"
			}
			name := t.Type
			if name == "" {
				name = "(sem tipo)"
			}
			fmt.Fprintf(w, "| %s | %d | %d | %d | %d | %s | %s |\n", name, t.Entries, t.Recover, t.Exhaust, t.Wait, dest, ext)
		}
	}
	fmt.Fprintf(w, "\n**Recuperaria, por fila de destino:** %s\n\n", counts(r.RecoverByQueue))
	fmt.Fprintf(w, "**Recuperaria com destino que pode enviar a uma pessoa:** %d (por idade: %s)\n\n", r.RecoverExternal, counts(r.AgeExternal))
	fmt.Fprintf(w, "**Recuperaria, por tentativas já feitas:** %s\n\n", counts(r.Attempts))
	fmt.Fprintf(w, "**Recuperaria, por idade da entrada:** %s\n\n", counts(r.Age))
	if r.RecoverUnknown > 0 {
		fmt.Fprintf(w, "**Atenção:** %d entradas seriam recuperadas sem rota conhecida.\n\n", r.RecoverUnknown)
	}
	if r.Census != nil {
		fmt.Fprintf(w, "**Último censo do Outbox (todas as entradas):** %s; pending atrasadas: %d\n", counts64(r.Census), r.PendingStale)
	}
}

func counts(m map[string]int) string {
	if len(m) == 0 {
		return "nenhuma"
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
	return strings.Join(parts, ", ")
}

func counts64(m map[string]int64) string {
	c := map[string]int{}
	for k, v := range m {
		c[k] = int(v)
	}
	return counts(c)
}
