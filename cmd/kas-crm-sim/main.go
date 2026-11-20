// kas-crm-sim is a simulated CRM for the Process Studio pilot. It consumes
// commands through the real pull API of the Outbox, applies each effect once
// (effect and receipt saved together in a state file), reports the result and
// only then acks. A confirmed crm.package.create_renewal creates one new
// package per command_key and reports its id as result_ref; with
// -drop-first-result-refs the first report of each command is lost on purpose,
// so KAS has to query the same command_key. Loopback only, fictitious tenants
// only: it is a test and demo tool, never a production component.
//
//	kas-crm-sim -kas http://127.0.0.1:8088 -service demo-crm-a -token-env DEMO_CRM_A_TOKEN \
//	    -consumer crm-simulado-a -state ./sim-a.json
package main

import (
	"context"
	"errors"
	"flag"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"kas-core-pilot/internal/crmsim"
)

func main() { os.Exit(run()) }

func run() int {
	base := flag.String("kas", "http://127.0.0.1:8088", "KAS base URL (loopback only)")
	service := flag.String("service", "", "service id (X-KAS-Service)")
	tokenEnv := flag.String("token-env", "", "environment variable holding the service token")
	consumer := flag.String("consumer", "", "event consumer id bound to the service")
	statePath := flag.String("state", "", "JSON file for receipts and effects (survives restarts)")
	reject := flag.String("reject-refs", "", "comma-separated business_refs whose commands are rejected")
	mute := flag.String("mute-refs", "", "comma-separated business_refs whose results are never reported")
	dropFirst := flag.String("drop-first-result-refs", "", "comma-separated business_refs (a trailing * matches a prefix) whose FIRST result report per command is lost after the effect (KAS must query the same command_key)")
	crash := flag.Bool("crash-after-effect", false, "exit with code 3 right after saving the first effect, before result and ack")
	once := flag.Bool("once", false, "drain what is due and exit")
	interval := flag.Duration("interval", 500*time.Millisecond, "poll interval")
	lease := flag.Int("lease-seconds", 10, "claim lease (5..300)")
	flag.Parse()

	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil)).With("component", "kas-crm-sim", "service", *service)
	token, _ := os.LookupEnv(*tokenEnv)
	sim, err := crmsim.New(crmsim.Config{
		BaseURL: *base, ServiceID: *service, Token: token, ConsumerID: *consumer, StatePath: *statePath,
		RejectRefs: set(*reject), MuteRefs: set(*mute), DropFirstResultRefs: set(*dropFirst), CrashAfterEffect: *crash, LeaseSeconds: *lease, Logger: logger,
	})
	if err != nil {
		logger.Error("invalid configuration", "reason", err.Error())
		return 2
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if *once {
		n, err := sim.Drain(ctx)
		if errors.Is(err, crmsim.ErrCrashAfterEffect) {
			logger.Warn("simulated crash after effect")
			return 3
		}
		if err != nil {
			logger.Error("drain failed", "reason", err.Error())
			return 1
		}
		logger.Info("drained", "acked", n)
		return 0
	}
	logger.Info("crm simulator running", "kas", *base)
	if err := sim.Run(ctx, *interval); errors.Is(err, crmsim.ErrCrashAfterEffect) {
		logger.Warn("simulated crash after effect")
		return 3
	}
	return 0
}

func set(csv string) map[string]bool {
	out := map[string]bool{}
	for _, v := range strings.Split(csv, ",") {
		if v = strings.TrimSpace(v); v != "" {
			out[v] = true
		}
	}
	return out
}
