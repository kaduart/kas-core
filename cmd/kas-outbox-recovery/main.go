// Command kas-outbox-recovery recovers transactional-outbox entries that the
// CRM dispatcher marked as failed and never selects again. It runs in shadow
// mode (read only, default) or executor mode (conditional writes). See
// docs/outbox-recovery/README.md.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
	"go.mongodb.org/mongo-driver/v2/mongo/readpref"

	"kas-core-pilot/internal/alerts"
	"kas-core-pilot/internal/legacy/crmalerts"
	"kas-core-pilot/internal/legacy/crmoutbox"
	"kas-core-pilot/internal/outboxrecovery"
)

func main() { os.Exit(run(context.Background(), os.Args[1:], os.Stdout, os.Stderr)) }

type cli struct {
	mode, uriEnv, db, collection, confirmDB, reportPath, permanent, unmark string
	interval, cycleTimeout, stalePending                                   time.Duration
	maxAttempts, maxRecoveries, batch, maxPages                            int
	once, allowRemote, ensureIndexes                                       bool
}

func parse(args []string, stderr io.Writer) (cli, error) {
	var c cli
	fs := flag.NewFlagSet("kas-outbox-recovery", flag.ContinueOnError)
	fs.SetOutput(stderr)
	def := outboxrecovery.DefaultConfig()
	fs.StringVar(&c.mode, "mode", "shadow", "shadow (read only) or executor (writes; only on a disposable database in this phase)")
	fs.StringVar(&c.uriEnv, "mongo-uri-env", "KAS_OUTBOX_MONGO_URI", "environment variable holding the MongoDB URI (never pass the URI as a flag)")
	fs.StringVar(&c.db, "db", "", "database name of the CRM (required)")
	fs.StringVar(&c.collection, "collection", crmoutbox.DefaultCollection, "outbox collection")
	fs.StringVar(&c.confirmDB, "executor-confirm-db", "", "executor mode and -unmark: must repeat -db")
	fs.BoolVar(&c.allowRemote, "allow-remote-executor", false, "executor mode: allow a non-loopback MongoDB host. Not authorised in this phase")
	fs.StringVar(&c.reportPath, "report", "", "append one JSON line per cycle (identifiers and counts only)")
	fs.StringVar(&c.permanent, "permanent-error-prefixes", strings.Join(def.PermanentErrorPrefixes, ","), "comma-separated lastError prefixes that are never retried")
	fs.StringVar(&c.unmark, "unmark", "", "reversal: remove exhaust annotations with this reason ('all' for every reason) and exit")
	fs.BoolVar(&c.ensureIndexes, "ensure-indexes", false, "create the scan index and exit (executor confirmation required)")
	fs.DurationVar(&c.interval, "interval", time.Minute, "time between cycles")
	fs.DurationVar(&c.cycleTimeout, "cycle-timeout", time.Minute, "upper bound for one cycle")
	fs.DurationVar(&c.stalePending, "stale-pending-after", 10*time.Minute, "shadow census: pending entries due for longer than this are counted as stale (no dispatcher running)")
	fs.IntVar(&c.maxAttempts, "max-attempts", def.MaxAttempts, "attempts at which an entry is exhausted instead of recovered")
	fs.IntVar(&c.maxRecoveries, "max-recoveries", def.MaxRecoveries, "recoveries of the same entry before it is exhausted")
	fs.IntVar(&c.batch, "batch", def.BatchSize, "entries per page")
	fs.IntVar(&c.maxPages, "max-pages", 10, "pages per cycle")
	fs.BoolVar(&c.once, "once", false, "run a single cycle and exit")
	if err := fs.Parse(args); err != nil {
		return c, err
	}
	if c.db == "" {
		return c, errors.New("-db is required")
	}
	return c, nil
}

func loopbackOnly(uri string) bool {
	if strings.HasPrefix(uri, "mongodb+srv://") {
		return false
	}
	u, err := url.Parse(uri)
	if err != nil {
		return false
	}
	for _, hp := range strings.Split(u.Host, ",") {
		h := hp
		if hh, _, err := net.SplitHostPort(hp); err == nil {
			h = hh
		}
		if h == "localhost" {
			continue
		}
		if ip := net.ParseIP(strings.Trim(h, "[]")); ip == nil || !ip.IsLoopback() {
			return false
		}
	}
	return true
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	logger := slog.New(slog.NewJSONHandler(stdout, nil))
	c, err := parse(args, stderr)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	mode, err := outboxrecovery.ParseMode(c.mode)
	if err != nil {
		logger.Error("invalid mode", "reason", err.Error())
		return 2
	}
	cfg := outboxrecovery.Config{MaxAttempts: c.maxAttempts, MaxRecoveries: c.maxRecoveries, BatchSize: c.batch}
	for _, p := range strings.Split(c.permanent, ",") {
		if p = strings.TrimSpace(p); p != "" {
			cfg.PermanentErrorPrefixes = append(cfg.PermanentErrorPrefixes, p)
		}
	}
	if err := cfg.Validate(); err != nil {
		logger.Error("invalid configuration", "reason", err.Error())
		return 2
	}
	uri := os.Getenv(c.uriEnv)
	if uri == "" {
		logger.Error("MongoDB URI not set", "env", c.uriEnv)
		return 2
	}
	writes := mode == outboxrecovery.ModeExecutor || c.unmark != "" || c.ensureIndexes
	if writes {
		if c.confirmDB != c.db {
			logger.Error("writes refused: -executor-confirm-db must repeat -db")
			return 2
		}
		if !loopbackOnly(uri) && !c.allowRemote {
			logger.Error("writes refused: MongoDB host is not loopback; -allow-remote-executor is not authorised in this phase")
			return 2
		}
	}
	client, err := mongo.Connect(options.Client().ApplyURI(uri).SetServerSelectionTimeout(10 * time.Second).SetAppName("kas-outbox-recovery"))
	if err != nil {
		logger.Error("connect", "reason", err.Error())
		return 1
	}
	defer client.Disconnect(context.Background())
	pctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	err = client.Ping(pctx, readpref.Primary())
	cancel()
	if err != nil {
		logger.Error("ping", "reason", err.Error())
		return 1
	}
	store := crmoutbox.New(client, c.db, c.collection, cfg)

	if c.ensureIndexes {
		if err := store.EnsureIndexes(ctx); err != nil {
			logger.Error("ensure indexes", "reason", err.Error())
			return 1
		}
		logger.Info("index ready", "name", "kas_outbox_recovery_scan")
		return 0
	}
	if c.unmark != "" {
		reason := c.unmark
		if reason == "all" {
			reason = ""
		}
		n, err := store.Unmark(ctx, reason)
		if err != nil {
			logger.Error("unmark", "reason", err.Error())
			return 1
		}
		logger.Info("unmarked", "entries", n, "reason", c.unmark)
		return 0
	}

	var report *json.Encoder
	if c.reportPath != "" {
		f, err := os.OpenFile(c.reportPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
		if err != nil {
			logger.Error("report", "reason", err.Error())
			return 1
		}
		defer f.Close()
		report = json.NewEncoder(f)
	}
	// Alertas passam pela central (dedup, cooldown, escalonamento) e saem no
	// log estruturado. Entrega num canal monitorado continua sendo um bloqueio
	// de ativação (ver o README).
	center := alerts.New(alerts.DefaultConfig(), alerts.LogSink{Logger: logger}, time.Now)
	hours := crmalerts.FonoInovaHours()
	r := &outboxrecovery.Runner{
		Config: cfg, Mode: mode, Store: store, Now: time.Now, MaxPages: c.maxPages, StalePendingAfter: c.stalePending,
		Alert: func(ctx context.Context, a outboxrecovery.Alert) error {
			_, err := center.Raise(ctx, crmalerts.OutboxExhausted(a))
			if errors.Is(err, alerts.ErrInvalidAlert) {
				return err
			}
			return nil // falha de entrega fica pendente na central e é tentada de novo
		},
	}
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()
	logger.Info("start", "mode", string(mode), "db", c.db, "collection", c.collection, "maxAttempts", cfg.MaxAttempts)
	for {
		cctx, cancel := context.WithTimeout(ctx, c.cycleTimeout)
		cy, err := r.RunCycle(cctx)
		cancel()
		if report != nil {
			_ = report.Encode(cy)
		}
		for _, a := range crmalerts.OutboxCycle(cy, hours) {
			_, _ = center.Raise(ctx, a)
		}
		if ferr := center.Flush(ctx); ferr != nil {
			logger.Warn("alert delivery pending", "reason", ferr.Error(), "pending", center.Pending())
		}
		if err != nil {
			logger.Error("cycle", "reason", err.Error(), "recovered", cy.Recovered, "exhausted", cy.Exhausted)
		} else {
			logger.Info("cycle", "scanned", cy.Scanned, "recovered", cy.Recovered, "exhausted", cy.Exhausted, "lostRaces", cy.LostRaces, "decisions", cy.Decisions)
		}
		if c.once {
			if err != nil {
				return 1
			}
			return 0
		}
		select {
		case <-ctx.Done():
			logger.Info("stop")
			return 0
		case <-time.After(c.interval):
		}
	}
}
