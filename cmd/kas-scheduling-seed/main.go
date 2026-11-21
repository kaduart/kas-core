// Command kas-scheduling-seed loads a FICTITIOUS agenda into a LOCAL KAS
// database, so the panel's Agenda screen has something to show (phase 4B).
// It refuses a non-loopback database, a database whose name suggests
// production, and any tenant outside the fictitious list. Running it again
// changes nothing: resources and services are upserts and every booking has a
// fixed idempotency key per day.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"strings"
	"time"

	"kas-core-pilot/internal/scheduling"
)

func main() { os.Exit(run(os.Args[1:], os.Getenv, time.Now, os.Stdout, os.Stderr)) }

// Only fictitious tenants. The Fono Inova agenda is never written by this tool.
var fictitious = map[string]bool{"clinica-aurora": true, "odonto-sorriso": true, "escola-demo": true}

func loopback(h string) bool {
	if h == "localhost" {
		return true
	}
	ip := net.ParseIP(strings.Trim(h, "[]"))
	return ip != nil && ip.IsLoopback()
}

func checkDSN(dsn string) error {
	u, err := url.Parse(dsn)
	if err != nil || (u.Scheme != "postgres" && u.Scheme != "postgresql") || !loopback(u.Hostname()) {
		return errors.New("the database must be a postgres:// URL on loopback")
	}
	if strings.Contains(strings.ToLower(u.Path), "prod") {
		return errors.New("refusing a database whose name suggests production")
	}
	return nil
}

type professional struct {
	id, label string
	days      []int // weekdays, 0 = Sunday
	from, to  int   // local minutes
	lunch     bool
}

var team = []professional{
	{"prof-helena", "Helena Prado · Fonoaudiologia", []int{1, 2, 3, 4, 5}, 8 * 60, 18 * 60, true},
	{"prof-davi", "Davi Moura · Psicologia", []int{1, 3, 5}, 13 * 60, 19 * 60, false},
	{"prof-lia", "Lia Campos · Terapia ocupacional", []int{2, 4, 6}, 8 * 60, 12 * 60, false},
	{"sala-2", "Sala 2 · Integração sensorial", []int{1, 2, 3, 4, 5}, 8 * 60, 18 * 60, false},
}

var services = []scheduling.Service{
	{ID: "avaliacao", Label: "Avaliação", DurationMin: 60, BufferMin: 15},
	{ID: "sessao", Label: "Sessão", DurationMin: 40, BufferMin: 10},
	{ID: "retorno", Label: "Retorno", DurationMin: 30, BufferMin: 0},
}

// A few bookings per working day of this week and next, at fixed local times.
var pattern = []struct {
	res, client, service, status string
	at                           int
}{
	{"prof-helena", "cli-1001", "sessao", scheduling.StatusConfirmed, 8*60 + 0},
	{"prof-helena", "cli-1002", "sessao", scheduling.StatusScheduled, 9*60 + 0},
	{"prof-helena", "cli-1003", "avaliacao", scheduling.StatusTentative, 14*60 + 0},
	{"prof-davi", "cli-2001", "sessao", scheduling.StatusScheduled, 13*60 + 0},
	{"prof-davi", "cli-2002", "retorno", scheduling.StatusConfirmed, 15*60 + 30},
	{"prof-lia", "cli-3001", "sessao", scheduling.StatusScheduled, 9*60 + 0},
	{"sala-2", "cli-1004", "sessao", scheduling.StatusScheduled, 10*60 + 0},
}

func run(args []string, getenv func(string) string, now func() time.Time, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("kas-scheduling-seed", flag.ContinueOnError)
	fs.SetOutput(stderr)
	tenant := fs.String("tenant", "clinica-aurora", "fictitious tenant to load")
	zone := fs.String("zone", "America/Sao_Paulo", "tenant time zone")
	dsnEnv := fs.String("database-url-env", "KAS_EVENTS_DATABASE_URL", "variable holding the local PostgreSQL URL")
	migrate := fs.Bool("migrate", false, "apply pending scheduling migrations first")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if !fictitious[*tenant] {
		fmt.Fprintf(stderr, "refused: %q is not a fictitious tenant (allowed: clinica-aurora, odonto-sorriso, escola-demo)\n", *tenant)
		return 2
	}
	dsn := getenv(*dsnEnv)
	if err := checkDSN(dsn); err != nil {
		fmt.Fprintln(stderr, "refused:", err)
		return 2
	}
	loc, err := time.LoadLocation(*zone)
	if err != nil {
		fmt.Fprintln(stderr, "invalid -zone")
		return 2
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	st, err := scheduling.Open(ctx, dsn, *migrate)
	if err != nil {
		fmt.Fprintln(stderr, "scheduling store unavailable:", err)
		return 1
	}
	defer st.Close()
	n, err := seed(ctx, st, *tenant, loc, now())
	if err != nil {
		fmt.Fprintln(stderr, "seed failed:", err)
		return 1
	}
	fmt.Fprintf(stdout, "agenda fictícia de %s: %d profissionais/salas, %d serviços, %d agendamentos novos\n", *tenant, len(team), len(services), n)
	return 0
}

func seed(ctx context.Context, st *scheduling.Store, tenant string, loc *time.Location, now time.Time) (int, error) {
	if err := st.PutSettings(ctx, tenant, scheduling.Settings{Zone: loc.String(), Holidays: []string{}, ClientExclusive: true}); err != nil {
		return 0, fmt.Errorf("settings: %w", err)
	}
	for _, p := range team {
		kind := "person"
		if strings.HasPrefix(p.id, "sala-") {
			kind = "room"
		}
		if err := st.PutResource(ctx, tenant, scheduling.Resource{ID: p.id, Kind: kind, Label: p.label, Capacity: 1, Active: true}); err != nil {
			return 0, fmt.Errorf("resource %s: %w", p.id, err)
		}
		var rules []scheduling.WeeklyRule
		for _, d := range p.days {
			if p.lunch {
				rules = append(rules, scheduling.WeeklyRule{Weekday: d, StartMin: p.from, EndMin: 12 * 60},
					scheduling.WeeklyRule{Weekday: d, StartMin: 13 * 60, EndMin: p.to})
			} else {
				rules = append(rules, scheduling.WeeklyRule{Weekday: d, StartMin: p.from, EndMin: p.to})
			}
		}
		if err := st.PutWeeklyRules(ctx, tenant, p.id, rules); err != nil {
			return 0, fmt.Errorf("rules %s: %w", p.id, err)
		}
	}
	for _, sv := range services {
		if err := st.PutService(ctx, tenant, sv); err != nil {
			return 0, fmt.Errorf("service %s: %w", sv.ID, err)
		}
	}
	today := now.In(loc)
	monday := time.Date(today.Year(), today.Month(), today.Day(), 0, 0, 0, 0, loc).AddDate(0, 0, -((int(today.Weekday()) + 6) % 7))
	created := 0
	for d := 0; d < 14; d++ {
		day := monday.AddDate(0, 0, d)
		for i, b := range pattern {
			p := find(b.res)
			if !works(p, int(day.Weekday())) {
				continue
			}
			start := time.Date(day.Year(), day.Month(), day.Day(), b.at/60, b.at%60, 0, 0, loc)
			if !start.After(now) {
				continue // the past is not rewritten
			}
			sv := svc(b.service)
			_, isNew, err := st.CreateBooking(ctx, tenant, scheduling.NewBooking{
				ResourceID: b.res, ClientRef: b.client, ServiceID: sv.ID, Status: b.status,
				Start: start, End: start.Add(time.Duration(sv.DurationMin) * time.Minute),
				IdempotencyKey: fmt.Sprintf("seed-%s-%d", day.Format("20060102"), i), Actor: "service:kas-scheduling-seed", RequireOpen: true,
			})
			switch {
			case errors.Is(err, scheduling.ErrSlotTaken), errors.Is(err, scheduling.ErrClientBusy), errors.Is(err, scheduling.ErrClosed):
				continue // someone already uses that time in the panel: keep their booking
			case err != nil:
				return created, fmt.Errorf("booking %s %s: %w", b.res, start.Format(time.RFC3339), err)
			}
			if isNew {
				created++
			}
		}
	}
	return created, nil
}

func find(id string) professional {
	for _, p := range team {
		if p.id == id {
			return p
		}
	}
	return professional{}
}

func works(p professional, wd int) bool {
	for _, d := range p.days {
		if d == wd {
			return true
		}
	}
	return false
}

func svc(id string) scheduling.Service {
	for _, s := range services {
		if s.ID == id {
			return s
		}
	}
	return scheduling.Service{}
}
