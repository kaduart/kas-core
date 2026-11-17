package main

import (
	"context"
	"flag"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"kas-core-pilot/internal/audit"
	"kas-core-pilot/internal/configstore"
	"kas-core-pilot/internal/events"
	"kas-core-pilot/internal/httpapi"
)

func main() {
	os.Exit(run())
}

func run() int {
	configPath := flag.String("config", "config.example.json", "path to non-secret KAS configuration JSON")
	auditDataDir := flag.String("audit-data-dir", "./data", "directory for local JSON Lines audit data")
	auditBackend := flag.String("audit-store", "jsonl", "audit storage backend: jsonl (default) or postgres")
	auditDatabaseURLEnv := flag.String("audit-database-url-env", "KAS_AUDIT_DATABASE_URL", "environment variable holding the PostgreSQL connection string (used with -audit-store postgres)")
	auditAutoMigrate := flag.Bool("audit-auto-migrate", true, "with -audit-store postgres: apply pending migrations at startup; set false when the runtime role has no DDL rights")
	auditMigrateOnly := flag.Bool("audit-migrate-only", false, "apply audit PostgreSQL migrations with the migration role and exit")
	auditMigrationURLEnv := flag.String("audit-migration-database-url-env", "KAS_AUDIT_MIGRATION_DATABASE_URL", "environment variable holding the migration-role PostgreSQL connection string (used with -audit-migrate-only)")
	eventsBackend := flag.String("events-store", "none", "event outbox storage: none (default, publication answers 503) or postgres")
	eventsDatabaseURLEnv := flag.String("events-database-url-env", "KAS_EVENTS_DATABASE_URL", "environment variable holding the events PostgreSQL connection string (used with -events-store postgres)")
	eventsAutoMigrate := flag.Bool("events-auto-migrate", true, "with -events-store postgres: apply pending migrations at startup; set false when the runtime role has no DDL rights")
	eventsMigrateOnly := flag.Bool("events-migrate-only", false, "apply events PostgreSQL migrations with the migration role and exit")
	eventsMigrationURLEnv := flag.String("events-migration-database-url-env", "KAS_EVENTS_MIGRATION_DATABASE_URL", "environment variable holding the events migration-role connection string (used with -events-migrate-only)")
	addr := flag.String("addr", "127.0.0.1:8088", "listen address")
	maxConcurrent := flag.Int("max-concurrent", 32, "maximum concurrent HTTP requests (1-128)")
	tlsCert := flag.String("tls-cert", "", "TLS certificate path (required for non-loopback listeners)")
	tlsKey := flag.String("tls-key", "", "TLS private key path (required for non-loopback listeners)")
	flag.Parse()

	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	if *auditMigrateOnly || *eventsMigrateOnly {
		if *auditMigrateOnly {
			if *auditBackend != "postgres" {
				logger.Error("-audit-migrate-only requires -audit-store postgres")
				return 2
			}
			dsn, found := os.LookupEnv(*auditMigrationURLEnv)
			if !found || dsn == "" {
				logger.Error("audit migration connection variable is not set", "env", *auditMigrationURLEnv)
				return 2
			}
			if err := audit.MigratePostgres(context.Background(), dsn); err != nil {
				logger.Error("audit migration failed", "reason", err.Error())
				return 1
			}
			logger.Info("audit migrations applied")
		}
		if *eventsMigrateOnly {
			if *eventsBackend != "postgres" {
				logger.Error("-events-migrate-only requires -events-store postgres")
				return 2
			}
			dsn, found := os.LookupEnv(*eventsMigrationURLEnv)
			if !found || dsn == "" {
				logger.Error("events migration connection variable is not set", "env", *eventsMigrationURLEnv)
				return 2
			}
			if err := events.MigratePostgres(context.Background(), dsn); err != nil {
				logger.Error("events migration failed", "reason", err.Error())
				return 1
			}
			logger.Info("events migrations applied")
		}
		return 0
	}
	if *maxConcurrent < 1 || *maxConcurrent > 128 {
		logger.Error("invalid server concurrency limit")
		return 2
	}
	if (*tlsCert == "") != (*tlsKey == "") {
		logger.Error("both TLS certificate and private key paths are required")
		return 2
	}
	if !isLoopbackAddr(*addr) && (*tlsCert == "" || *tlsKey == "") {
		logger.Error("TLS is required when listening outside loopback")
		return 2
	}

	file, err := os.Open(*configPath)
	if err != nil {
		logger.Error("configuration file unavailable", "path", *configPath)
		return 2
	}
	defer file.Close()

	store, err := configstore.Load(file, os.LookupEnv)
	if err != nil {
		logger.Error("configuration rejected", "reason", err.Error())
		return 2
	}
	var auditStore interface {
		audit.Repository
		Close() error
	}
	switch *auditBackend {
	case "jsonl":
		auditStore, err = audit.OpenJSONL(*auditDataDir)
	case "postgres":
		dsn, found := os.LookupEnv(*auditDatabaseURLEnv)
		if !found || dsn == "" {
			logger.Error("audit database connection variable is not set", "env", *auditDatabaseURLEnv)
			return 2
		}
		if *auditAutoMigrate {
			auditStore, err = audit.OpenPostgres(context.Background(), dsn)
		} else {
			auditStore, err = audit.OpenPostgresVerified(context.Background(), dsn)
		}
	default:
		logger.Error("invalid audit store; use jsonl or postgres")
		return 2
	}
	if err != nil {
		logger.Error("audit store unavailable", "reason", err.Error())
		return 2
	}
	defer func() {
		if err := auditStore.Close(); err != nil {
			logger.Error("audit store close failed; lock retained when ownership is uncertain", "error", err.Error())
		}
	}()
	auditService := audit.NewService(auditStore)
	api := httpapi.NewHandlerWithAudit(store, auditService, *maxConcurrent, logger)

	switch *eventsBackend {
	case "none":
	case "postgres":
		dsn, found := os.LookupEnv(*eventsDatabaseURLEnv)
		if !found || dsn == "" {
			logger.Error("events database connection variable is not set", "env", *eventsDatabaseURLEnv)
			return 2
		}
		var eventStore *events.PostgresStore
		if *eventsAutoMigrate {
			eventStore, err = events.OpenPostgres(context.Background(), dsn)
		} else {
			eventStore, err = events.OpenPostgresVerified(context.Background(), dsn)
		}
		if err != nil {
			logger.Error("event store unavailable", "reason", err.Error())
			return 2
		}
		defer func() {
			if err := eventStore.Close(); err != nil {
				logger.Error("event store close failed", "error", err.Error())
			}
		}()
		api.EnableEvents(events.NewService(eventStore, store))
	default:
		logger.Error("invalid events store; use none or postgres")
		return 2
	}

	server := &http.Server{
		Addr:              *addr,
		Handler:           api,
		ReadHeaderTimeout: 2 * time.Second,
		ReadTimeout:       3 * time.Second,
		WriteTimeout:      3 * time.Second,
		IdleTimeout:       30 * time.Second,
		MaxHeaderBytes:    8 << 10,
	}
	logger.Info("KAS Core pilot listening", "addr", *addr, "config_version", store.Version())
	shutdownSignal, stopSignals := newShutdownSignalContext()
	defer stopSignals()
	serveDone := make(chan error, 1)
	go func() {
		var serveErr error
		if *tlsCert != "" {
			serveErr = server.ListenAndServeTLS(*tlsCert, *tlsKey)
		} else {
			serveErr = server.ListenAndServe()
		}
		serveDone <- serveErr
	}()
	select {
	case <-shutdownSignal.Done():
		api.BeginDrain()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		shutdownErr := server.Shutdown(ctx)
		cancel()
		if shutdownErr != nil {
			logger.Error("HTTP shutdown grace period expired; waiting for accepted handlers before closing audit storage", "error", shutdownErr.Error())
		}
		api.WaitForRequests()
		serveErr := <-serveDone
		if serveErr != nil && serveErr != http.ErrServerClosed {
			logger.Error("HTTP server stopped", "error", serveErr.Error())
			return 1
		}
		if shutdownErr != nil {
			return 1
		}
	case serveErr := <-serveDone:
		api.BeginDrain()
		api.WaitForRequests()
		if serveErr != nil && serveErr != http.ErrServerClosed {
			logger.Error("HTTP server stopped", "error", serveErr.Error())
			return 1
		}
	}
	return 0
}

func newShutdownSignalContext() (context.Context, context.CancelFunc) {
	return signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
}

func isLoopbackAddr(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
