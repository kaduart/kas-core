// Command kas-event-reaper is the KAS port of the CRM cron
// crons/eventReaper.cron.js. It runs in shadow mode (read only, default) or in
// executor mode (fenced writes). See docs/event-reaper/README.md.
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
	"net/http"
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
	"kas-core-pilot/internal/eventreaper"
	"kas-core-pilot/internal/legacy/crmalerts"
	"kas-core-pilot/internal/legacy/crmeventstore"
	"kas-core-pilot/internal/metrics"
)

func main() { os.Exit(run(os.Args[1:], os.Stdout, os.Stderr)) }

type cliConfig struct {
	mode, protocol, legacyFence, requireReadOnly, uriEnv, db, collection, leaseCollection, confirmDB, reportPath, metricsAddr, holder string
	interval, warmup, threshold, leaseTTL, cycleTimeout                                                                               time.Duration
	maxAttempts, maxRecoveries, batch                                                                                                 int
	once, allowRemoteExecutor, requireCRMNode                                                                                         bool
	crmNodeMaxAge                                                                                                                     time.Duration
}

func parse(args []string, stderr io.Writer) (cliConfig, error) {
	var c cliConfig
	fs := flag.NewFlagSet("kas-event-reaper", flag.ContinueOnError)
	fs.SetOutput(stderr)
	host, _ := os.Hostname()
	fs.StringVar(&c.mode, "mode", "shadow", "shadow (read only) or executor (writes; only on a disposable database in this phase)")
	fs.StringVar(&c.protocol, "protocol", "v1", "v1: CRM reaper as in production (createdAt > threshold). v2: CRM with execution ownership (expired lease, recoveryCount, dead_letter); must match the CRM build")
	fs.StringVar(&c.uriEnv, "mongo-uri-env", "KAS_REAPER_MONGO_URI", "environment variable holding the MongoDB URI (never pass the URI as a flag)")
	fs.StringVar(&c.db, "db", "", "database name of the CRM event store (required)")
	fs.StringVar(&c.collection, "collection", crmeventstore.DefaultCollection, "legacy event store collection")
	fs.StringVar(&c.leaseCollection, "lease-collection", crmeventstore.DefaultLeaseCollection, "executor lease collection (executor mode only)")
	fs.StringVar(&c.confirmDB, "executor-confirm-db", "", "executor mode: must repeat -db, as a guard against pointing at the wrong database")
	fs.BoolVar(&c.allowRemoteExecutor, "allow-remote-executor", false, "executor mode: allow a non-loopback MongoDB host. Not authorised in this phase")
	fs.StringVar(&c.reportPath, "report", "", "append one JSON line per cycle to this file (identifiers and states only)")
	fs.StringVar(&c.metricsAddr, "metrics-addr", "", "serve /metrics on this loopback address, e.g. 127.0.0.1:9465")
	fs.StringVar(&c.holder, "holder", fmt.Sprintf("%s-%d", host, os.Getpid()), "executor name shown in the lease document")
	fs.DurationVar(&c.interval, "interval", 5*time.Minute, "time between cycles (legacy: 5m)")
	fs.DurationVar(&c.warmup, "warmup", 2*time.Minute, "delay before the first cycle (legacy: 2m)")
	fs.DurationVar(&c.threshold, "stuck-threshold", 15*time.Minute, "createdAt age that makes a processing event eligible (legacy: 15m)")
	fs.DurationVar(&c.leaseTTL, "lease-ttl", 12*time.Minute, "executor lease lifetime; longer than -interval so the holder keeps it")
	fs.DurationVar(&c.cycleTimeout, "cycle-timeout", 2*time.Minute, "upper bound for one cycle; a database that stops answering is reported as an error")
	fs.IntVar(&c.maxAttempts, "max-attempts", 3, "attempts below this go back to pending, others to failed (legacy: 3)")
	fs.StringVar(&c.legacyFence, "legacy-fence", "", "protocol v2: RFC 3339 instant after which no pre-ownership CRM process runs (CRM EVENT_LEGACY_FENCE_AT); empty: ownerless processing events are never recovered")
	fs.IntVar(&c.maxRecoveries, "max-recoveries", 3, "protocol v2: recoveries before dead_letter (CRM MAX_RECOVERIES: 3)")
	fs.IntVar(&c.batch, "batch", 100, "events per cycle (legacy: 100)")
	fs.BoolVar(&c.once, "once", false, "run a single cycle without warmup and exit")
	fs.BoolVar(&c.requireCRMNode, "require-crm-node-lease", true, "executor mode: act (and fence every write) only while the CRM Node reaper refreshes participants.crmNode in the same lease document (EVENT_REAPER_SHARED_LEASE=on). On by default; prevents two owners when the Node flag is absent/off or points elsewhere. =false is accepted only on a loopback MongoDB host (disposable test database with no CRM Node), never together with -allow-remote-executor")
	fs.StringVar(&c.requireReadOnly, "require-read-only", "auto", "shadow mode: prove with connectionStatus that the database user can only read, and refuse to start otherwise. auto: on for any non-loopback host; on; off (accepted only on a loopback host)")
	fs.DurationVar(&c.crmNodeMaxAge, "crm-node-marker-max-age", 15*time.Minute, "with -require-crm-node-lease: maximum age of the CRM Node marker (server clock)")
	if err := fs.Parse(args); err != nil {
		return c, err
	}
	if c.db == "" {
		return c, errors.New("-db is required")
	}
	return c, nil
}

type startLine struct {
	Kind             string           `json:"kind"`
	At               time.Time        `json:"at"`
	Mode             eventreaper.Mode `json:"mode"`
	Protocol         string           `json:"protocol"`
	LegacyFence      string           `json:"legacy_fence,omitempty"`
	DB               string           `json:"db"`
	Collection       string           `json:"collection"`
	Interval         string           `json:"interval"`
	StuckThreshold   string           `json:"stuck_threshold"`
	Batch            int              `json:"batch"`
	HostClass        string           `json:"host_class"`
	ReadOnlyRequired bool             `json:"read_only_required"`
	ReadOnly         *readOnlyProof   `json:"read_only,omitempty"`
}

type cycleLine struct {
	Kind string `json:"kind"`
	eventreaper.CycleReport
	Proc procStats `json:"proc"`
}

func isLoopbackHost(uri string) (bool, error) {
	if strings.HasPrefix(uri, "mongodb+srv://") {
		return false, nil
	}
	u, err := url.Parse(uri)
	if err != nil {
		return false, fmt.Errorf("invalid MongoDB URI")
	}
	for _, hp := range strings.Split(u.Host, ",") {
		h := hp
		if hh, _, err := net.SplitHostPort(hp); err == nil {
			h = hh
		}
		if h != "localhost" {
			ip := net.ParseIP(strings.Trim(h, "[]"))
			if ip == nil || !ip.IsLoopback() {
				return false, nil
			}
		}
	}
	return true, nil
}

func run(args []string, stdout, stderr io.Writer) int {
	logger := slog.New(slog.NewJSONHandler(stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	c, err := parse(args, stderr)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	mode, err := eventreaper.ParseMode(c.mode)
	if err != nil {
		logger.Error("invalid mode", "reason", err.Error())
		return 2
	}
	protocol, err := eventreaper.ParseProtocol(c.protocol)
	if err != nil {
		logger.Error("invalid protocol", "reason", err.Error())
		return 2
	}
	var fence time.Time
	if c.legacyFence != "" {
		if fence, err = time.Parse(time.RFC3339, c.legacyFence); err != nil {
			logger.Error("invalid -legacy-fence", "reason", "use RFC 3339, e.g. 2026-11-20T03:00:00Z")
			return 2
		}
	}
	cfg := eventreaper.Config{StuckThreshold: c.threshold, MaxAttemptsForRetry: c.maxAttempts, BatchSize: c.batch, Protocol: protocol, MaxRecoveries: c.maxRecoveries, LegacyFence: fence}
	if err := cfg.Validate(); err != nil {
		logger.Error("invalid configuration", "reason", err.Error())
		return 2
	}
	uri, ok := os.LookupEnv(c.uriEnv)
	if !ok || uri == "" {
		logger.Error("MongoDB URI variable is not set", "env", c.uriEnv)
		return 2
	}
	loop, err := isLoopbackHost(uri)
	if err != nil {
		logger.Error("invalid MongoDB URI", "env", c.uriEnv)
		return 2
	}
	if mode == eventreaper.ModeExecutor {
		if c.confirmDB != c.db {
			logger.Error("executor mode requires -executor-confirm-db equal to -db")
			return 2
		}
		if !loop && !c.allowRemoteExecutor {
			logger.Error("executor mode refuses a non-loopback MongoDB host; this phase only runs the executor on a disposable local database")
			return 2
		}
		if !c.requireCRMNode && (!loop || c.allowRemoteExecutor) {
			logger.Error("executor without the CRM Node guard (-require-crm-node-lease=false) is only allowed on a loopback MongoDB host, never with -allow-remote-executor: on a real CRM database it could become a second owner")
			return 2
		}
		if c.leaseTTL <= c.interval {
			logger.Warn("lease TTL is not longer than the interval; the lease will change hands between cycles", "lease_ttl", c.leaseTTL.String(), "interval", c.interval.String())
		}
	}

	var mustProveReadOnly bool
	switch c.requireReadOnly {
	case "auto":
		mustProveReadOnly = mode == eventreaper.ModeShadow && !loop
	case "on":
		mustProveReadOnly = mode == eventreaper.ModeShadow
	case "off":
		if mode == eventreaper.ModeShadow && !loop {
			logger.Error("-require-read-only=off is only accepted on a loopback host: a shadow on a real database must prove it cannot write")
			return 2
		}
	default:
		logger.Error("invalid -require-read-only", "reason", "use auto, on or off")
		return 2
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	clientOpts := options.Client().ApplyURI(uri).SetAppName("kas-event-reaper").SetServerSelectionTimeout(10 * time.Second)
	if mode == eventreaper.ModeShadow {
		// Shadow reads from a secondary when one exists, to stay off the primary.
		clientOpts.SetReadPreference(readpref.SecondaryPreferred())
	}
	client, err := mongo.Connect(clientOpts)
	if err != nil {
		logger.Error("mongo connect failed", "reason", err.Error())
		return 1
	}
	defer func() {
		dctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = client.Disconnect(dctx)
	}()

	var proof *readOnlyProof
	if mustProveReadOnly {
		pctx, cancel := context.WithTimeout(ctx, 20*time.Second)
		p, err := proveReadOnly(pctx, client)
		cancel()
		if err != nil {
			logger.Error("shadow_refused_not_read_only", "reason", err.Error(), "roles", p.Roles, "actions", p.Actions)
			return 2
		}
		proof = &p
		logger.Info("read_only_confirmed", "users", p.Users, "roles", p.Roles, "actions", p.Actions)
	}

	var lease *crmeventstore.Lease
	if mode == eventreaper.ModeExecutor {
		lease = crmeventstore.NewLease(client, c.db, c.leaseCollection, c.holder, c.leaseTTL)
		if c.requireCRMNode {
			lease.RequireCRMNode(c.crmNodeMaxAge)
			logger.Info("executor requires the CRM Node reaper on the shared lease", "max_marker_age", c.crmNodeMaxAge.String(), "lease_collection", c.leaseCollection)
		} else {
			logger.Warn("executor WITHOUT the CRM Node guard (-require-crm-node-lease=false): allowed only on this loopback test database; never part of the activation procedure")
		}
		defer func() {
			rctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := lease.Release(rctx); err != nil {
				logger.Warn("lease release failed", "reason", err.Error())
			}
		}()
	}
	stats := eventreaper.NewStats()
	r := &eventreaper.Runner{Config: cfg, Mode: mode, Store: crmeventstore.New(client, c.db, c.collection, lease), Logger: logger, Stats: stats, CycleTimeout: c.cycleTimeout}
	if lease != nil {
		r.Lease = lease
	}

	var report func(eventreaper.CycleReport)
	if c.reportPath != "" {
		f, err := os.OpenFile(c.reportPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
		if err != nil {
			logger.Error("cannot open report file", "reason", err.Error())
			return 1
		}
		defer f.Close()
		enc := json.NewEncoder(f)
		hostClass := "remote"
		if loop {
			hostClass = "loopback"
		}
		// Header line: what this run was allowed to do. The summary tool
		// (kas-shadow-summary) requires one per run, with read_only proven.
		_ = enc.Encode(startLine{Kind: "start", At: time.Now().UTC(), Mode: mode, Protocol: string(protocol), LegacyFence: c.legacyFence,
			DB: c.db, Collection: c.collection, Interval: c.interval.String(), StuckThreshold: c.threshold.String(), Batch: c.batch,
			HostClass: hostClass, ReadOnlyRequired: mustProveReadOnly, ReadOnly: proof})
		defer func() { _ = enc.Encode(map[string]any{"kind": "stop", "at": time.Now().UTC()}) }()
		report = func(rep eventreaper.CycleReport) {
			_ = enc.Encode(cycleLine{Kind: "cycle", CycleReport: rep, Proc: readProcStats()})
		}
	}

	if c.metricsAddr != "" {
		if ok, _ := isLoopbackHost("mongodb://" + c.metricsAddr); !ok {
			logger.Error("-metrics-addr must be a loopback address")
			return 2
		}
		reg := metrics.NewRegistry()
		for _, col := range stats.Collectors() {
			reg.Register(col)
		}
		mux := http.NewServeMux()
		mux.HandleFunc("GET /metrics", func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", metrics.ContentType)
			_, _ = reg.WriteTo(w)
		})
		srv := &http.Server{Addr: c.metricsAddr, Handler: mux, ReadHeaderTimeout: 5 * time.Second}
		go func() {
			if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
				logger.Error("metrics listener failed", "reason", err.Error())
			}
		}()
		defer srv.Close()
	}

	logger.Info("event_reaper_start", "mode", mode, "db", c.db, "collection", c.collection,
		"interval", c.interval.String(), "warmup", c.warmup.String(), "stuck_threshold", c.threshold.String(),
		"max_attempts", c.maxAttempts, "batch", c.batch, "once", c.once)
	// Central de alertas: divergência da sombra, erro de ciclo e de transição
	// saem no log estruturado com dedup e cooldown. Canal monitorado é
	// bloqueio de ativação.
	center := alerts.New(alerts.DefaultConfig(), alerts.LogSink{Logger: logger}, time.Now)
	fileReport := report
	report = func(rep eventreaper.CycleReport) {
		if fileReport != nil {
			fileReport(rep)
		}
		for _, a := range crmalerts.ReaperCycle(rep) {
			_, _ = center.Raise(ctx, a)
		}
		_ = center.Flush(ctx)
	}
	if c.once {
		cctx, cancel := context.WithTimeout(ctx, c.cycleTimeout)
		rep := r.RunCycle(cctx)
		cancel()
		if report != nil {
			report(rep)
		}
		if rep.Err != "" {
			return 1
		}
		return 0
	}
	r.Loop(ctx, c.warmup, c.interval, report)
	logger.Info("event_reaper_stop")
	return 0
}
