package main

import (
	"os"
	"syscall"
	"testing"
	"time"
)

func TestSIGTERMRequestsGracefulShutdown(t *testing.T) {
	ctx, stop := newShutdownSignalContext()
	defer stop()

	process, err := os.FindProcess(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	if err := process.Signal(syscall.SIGTERM); err != nil {
		t.Fatalf("send SIGTERM to test process: %v", err)
	}

	select {
	case <-ctx.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("shutdown context was not canceled by SIGTERM")
	}
}
