package main

import (
	"bytes"
	"strings"
	"testing"
)

func runWith(t *testing.T, env map[string]string, args ...string) (int, string) {
	t.Helper()
	for k, v := range env {
		t.Setenv(k, v)
	}
	var out, errb bytes.Buffer
	code := run(args, &out, &errb)
	return code, out.String() + errb.String()
}

func TestExecutorRefusesARemoteHost(t *testing.T) {
	code, out := runWith(t, map[string]string{"KAS_REAPER_MONGO_URI": "mongodb://cluster.example.net:27017"},
		"-mode", "executor", "-db", "x", "-executor-confirm-db", "x", "-once")
	if code != 2 || !strings.Contains(out, "non-loopback") {
		t.Fatalf("code=%d out=%s", code, out)
	}
	code, out = runWith(t, map[string]string{"KAS_REAPER_MONGO_URI": "mongodb+srv://cluster.example.net"},
		"-mode", "executor", "-db", "x", "-executor-confirm-db", "x", "-once")
	if code != 2 || !strings.Contains(out, "non-loopback") {
		t.Fatalf("srv: code=%d out=%s", code, out)
	}
}

func TestExecutorRequiresTheDatabaseConfirmation(t *testing.T) {
	code, out := runWith(t, map[string]string{"KAS_REAPER_MONGO_URI": "mongodb://127.0.0.1:27017"},
		"-mode", "executor", "-db", "crm", "-once")
	if code != 2 || !strings.Contains(out, "executor-confirm-db") {
		t.Fatalf("code=%d out=%s", code, out)
	}
}

func TestURIComesOnlyFromTheEnvironment(t *testing.T) {
	t.Setenv("KAS_REAPER_MONGO_URI", "")
	code, out := runWith(t, nil, "-db", "x", "-once")
	if code != 2 || !strings.Contains(out, "not set") {
		t.Fatalf("code=%d out=%s", code, out)
	}
}

func TestRejectsBadModeAndConfig(t *testing.T) {
	env := map[string]string{"KAS_REAPER_MONGO_URI": "mongodb://127.0.0.1:27017"}
	if code, _ := runWith(t, env, "-db", "x", "-mode", "write"); code != 2 {
		t.Fatal("bad mode accepted")
	}
	if code, _ := runWith(t, env, "-db", "x", "-batch", "0"); code != 2 {
		t.Fatal("bad batch accepted")
	}
	if code, _ := runWith(t, env); code != 2 {
		t.Fatal("missing -db accepted")
	}
}

func TestLoopbackDetection(t *testing.T) {
	for uri, want := range map[string]bool{
		"mongodb://localhost:27017":                 true,
		"mongodb://127.0.0.1:27017,127.0.0.1:27018": true,
		"mongodb://[::1]:27017":                     true,
		"mongodb://127.0.0.1:27017,10.0.0.5:27017":  false,
		"mongodb://user:pw@producao.example.net/db": false,
		"mongodb+srv://producao.example.net/?x=1":   false,
	} {
		got, err := isLoopbackHost(uri)
		if err != nil || got != want {
			t.Fatalf("%s: got %v err %v", uri, got, err)
		}
	}
}
