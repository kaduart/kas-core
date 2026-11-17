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
	"kas-core-pilot/internal/httpapi"
)

func main() {
	os.Exit(run())
}

func run() int {
	configPath := flag.String("config", "config.example.json", "path to non-secret KAS configuration JSON")
	auditDataDir := flag.String("audit-data-dir", "./data", "directory for local JSON Lines audit data")
	addr := flag.String("addr", "127.0.0.1:8088", "listen address")
	maxConcurrent := flag.Int("max-concurrent", 32, "maximum concurrent HTTP requests (1-128)")
	tlsCert := flag.String("tls-cert", "", "TLS certificate path (required for non-loopback listeners)")
	tlsKey := flag.String("tls-key", "", "TLS private key path (required for non-loopback listeners)")
	flag.Parse()

	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
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
	auditStore, err := audit.OpenJSONL(*auditDataDir)
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
