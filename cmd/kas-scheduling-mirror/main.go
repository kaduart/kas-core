// kas-scheduling-mirror copies the CRM agenda, read-only, into the KAS
// Agenda mirror of one tenant (phase 2). In this phase it works only between
// LOCAL databases: the MongoDB and the PostgreSQL must be on a loopback
// address, and a database whose name suggests production is refused. A real
// copy needs a separate authorization (see docs/agenda/FASE3B_IMPORTACAO_PROTEGIDA.md).
//
// Secrets come from environment variables, never from flags:
//
//	KAS_MIRROR_MONGO_URI   mongodb://127.0.0.1:27018/?directConnection=true
//	KAS_MIRROR_PG_URL      postgres://kas:...@127.0.0.1:5432/kas?sslmode=disable
//	KAS_MIRROR_KEY         at least 32 bytes; the same key gives the same opaque ids
//
//	kas-scheduling-mirror -mode full -tenant clinica-aurora -mongo-db fono_espelho
//	kas-scheduling-mirror -mode incremental -tenant clinica-aurora -mongo-db fono_espelho
//	kas-scheduling-mirror -mode reconcile -from 2027-03-01 -to 2027-04-01 ...
//	kas-scheduling-mirror -mode verify -from 2027-03-01 -to 2027-04-01 ...
//
// The output is one JSON report with counts only (and, for verify, opaque ids).
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
	"regexp"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
	"go.mongodb.org/mongo-driver/v2/mongo/readpref"

	"kas-core-pilot/internal/legacy/crmagenda"
	"kas-core-pilot/internal/scheduling"
)

func main() { os.Exit(run(os.Args[1:], os.Getenv, os.Stdout, os.Stderr)) }

type config struct {
	mode, tenant, mongoURI, mongoDB, pgURL, zone string
	key                                          []byte
	batch                                        int64
	from, to                                     time.Time
	minGap, lag                                  time.Duration
	maxListed                                    int
	migrate                                      bool
}

var dbName = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,60}$`)

func loopbackHost(h string) bool {
	if h == "localhost" {
		return true
	}
	ip := net.ParseIP(strings.Trim(h, "[]"))
	return ip != nil && ip.IsLoopback()
}

func (c config) validate() error {
	switch c.mode {
	case "full", "incremental", "reconcile", "verify":
	default:
		return errors.New("-mode must be full, incremental, reconcile or verify")
	}
	if !scheduling.ValidRef(c.tenant) {
		return errors.New("-tenant is required")
	}
	if len(c.key) < 32 {
		return errors.New("KAS_MIRROR_KEY must hold at least 32 bytes")
	}
	u, err := url.Parse(c.mongoURI)
	if err != nil || u.Scheme != "mongodb" {
		return errors.New("KAS_MIRROR_MONGO_URI must be a plain mongodb:// URI (no +srv)")
	}
	for _, hp := range strings.Split(u.Host, ",") {
		h, _, err := net.SplitHostPort(hp)
		if err != nil {
			h = hp
		}
		if !loopbackHost(h) {
			return errors.New("phase 2 reads only a local MongoDB (loopback); a remote source needs a new authorization")
		}
	}
	if !dbName.MatchString(c.mongoDB) || strings.Contains(strings.ToLower(c.mongoDB), "prod") {
		return errors.New("-mongo-db must be a local copy's name, never a production database")
	}
	pu, err := url.Parse(c.pgURL)
	if err != nil || (pu.Scheme != "postgres" && pu.Scheme != "postgresql") || !loopbackHost(pu.Hostname()) {
		return errors.New("KAS_MIRROR_PG_URL must be a postgres:// URL on loopback in phase 2")
	}
	if c.batch < 1 || c.batch > crmagenda.MaxBatch {
		return fmt.Errorf("-batch must be 1..%d", crmagenda.MaxBatch)
	}
	if c.mode == "reconcile" || c.mode == "verify" {
		if c.from.IsZero() || !c.to.After(c.from) || c.to.Sub(c.from) > crmagenda.MaxWindow {
			return errors.New("-from and -to (YYYY-MM-DD) must make a window of at most 400 days")
		}
	}
	if c.minGap < 0 || c.lag < 0 {
		return errors.New("-min-gap and -safety-lag must not be negative")
	}
	return nil
}

func run(args []string, getenv func(string) string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("kas-scheduling-mirror", flag.ContinueOnError)
	fs.SetOutput(stderr)
	mode := fs.String("mode", "", "full, incremental, reconcile or verify")
	tenant := fs.String("tenant", "", "KAS tenant that receives the mirror")
	mongoDB := fs.String("mongo-db", "", "local MongoDB database holding the CRM copy")
	zone := fs.String("zone", "America/Sao_Paulo", "clinic time zone")
	batch := fs.Int64("batch", 500, "rows per batch (memory bound)")
	from := fs.String("from", "", "window start, local day YYYY-MM-DD (reconcile, verify)")
	to := fs.String("to", "", "window end, exclusive, local day YYYY-MM-DD (reconcile, verify)")
	minGap := fs.Duration("min-gap", 10*time.Minute, "a missing row is deleted only if it was missing at least this long before")
	lag := fs.Duration("safety-lag", 5*time.Minute, "incremental cursor starts this long before the full load began")
	maxListed := fs.Int("max-divergences", 50, "divergences listed in the verify report")
	migrate := fs.Bool("migrate", false, "apply the Agenda migrations before running (local only)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	cfg := config{mode: *mode, tenant: *tenant, mongoURI: getenv("KAS_MIRROR_MONGO_URI"), mongoDB: *mongoDB, pgURL: getenv("KAS_MIRROR_PG_URL"),
		zone: *zone, key: []byte(getenv("KAS_MIRROR_KEY")), batch: *batch, minGap: *minGap, lag: *lag, maxListed: *maxListed, migrate: *migrate}
	loc, err := time.LoadLocation(cfg.zone)
	if err != nil {
		fmt.Fprintln(stderr, "invalid -zone")
		return 2
	}
	if *from != "" || *to != "" {
		f, err1 := time.ParseInLocation("2006-01-02", *from, loc)
		t, err2 := time.ParseInLocation("2006-01-02", *to, loc)
		if err1 != nil || err2 != nil {
			fmt.Fprintln(stderr, "invalid configuration: -from and -to must be YYYY-MM-DD")
			return 2
		}
		cfg.from, cfg.to = f, t
	}
	if err := cfg.validate(); err != nil {
		fmt.Fprintln(stderr, "invalid configuration:", err)
		return 2
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Hour)
	defer cancel()
	out, err := execute(ctx, cfg, slog.New(slog.NewJSONHandler(stderr, nil)))
	if out != nil {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(out)
	}
	if err != nil {
		fmt.Fprintln(stderr, "mirror stopped:", err)
		if errors.Is(err, crmagenda.ErrBusy) {
			return 3
		}
		return 1
	}
	return 0
}

func execute(ctx context.Context, cfg config, log *slog.Logger) (any, error) {
	client, err := mongo.Connect(options.Client().ApplyURI(cfg.mongoURI).SetServerSelectionTimeout(10 * time.Second).
		SetReadPreference(readpref.SecondaryPreferred()).SetAppName("kas-scheduling-mirror"))
	if err != nil {
		return nil, fmt.Errorf("connect source: %w", err)
	}
	defer client.Disconnect(context.Background())
	store, err := scheduling.Open(ctx, cfg.pgURL, cfg.migrate)
	if err != nil {
		return nil, fmt.Errorf("open mirror: %w", err)
	}
	defer store.Close()
	mapper, err := crmagenda.NewMapper(cfg.key, cfg.zone)
	if err != nil {
		return nil, err
	}
	im := &crmagenda.Importer{Reader: crmagenda.NewReader(client.Database(cfg.mongoDB)), Mapper: mapper, Store: store,
		Tenant: cfg.tenant, Batch: cfg.batch, SafetyLag: cfg.lag, Log: log}
	switch cfg.mode {
	case "full":
		return im.Full(ctx)
	case "incremental":
		return im.Incremental(ctx)
	case "reconcile":
		return im.Reconcile(ctx, cfg.from, cfg.to, cfg.minGap)
	default:
		return im.Verify(ctx, cfg.from, cfg.to, cfg.zone, cfg.maxListed)
	}
}
