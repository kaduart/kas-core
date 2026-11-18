package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func TestWritesAreRefusedWithoutConfirmationOrOnARemoteHost(t *testing.T) {
	for _, tc := range []struct {
		name, uri string
		args      []string
		want      string
	}{
		{"executor without confirmation", "mongodb://127.0.0.1:1", []string{"-mode", "executor", "-db", "crm"}, "must repeat -db"},
		{"executor with the wrong confirmation", "mongodb://127.0.0.1:1", []string{"-mode", "executor", "-db", "crm", "-executor-confirm-db", "crm2"}, "must repeat -db"},
		{"unmark without confirmation", "mongodb://127.0.0.1:1", []string{"-db", "crm", "-unmark", "all"}, "must repeat -db"},
		{"executor on Atlas", "mongodb+srv://x.mongodb.net", []string{"-mode", "executor", "-db", "crm", "-executor-confirm-db", "crm"}, "not loopback"},
		{"executor on a remote host", "mongodb://10.0.0.5:27017", []string{"-mode", "executor", "-db", "crm", "-executor-confirm-db", "crm"}, "not loopback"},
		{"no URI", "", []string{"-db", "crm"}, "URI not set"},
		{"bad mode", "mongodb://127.0.0.1:1", []string{"-db", "crm", "-mode", "write"}, "invalid mode"},
		{"bad config", "mongodb://127.0.0.1:1", []string{"-db", "crm", "-max-attempts", "0"}, "invalid configuration"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("KAS_OUTBOX_MONGO_URI", tc.uri)
			var out, errb bytes.Buffer
			if code := run(context.Background(), tc.args, &out, &errb); code != 2 {
				t.Fatalf("exit %d, want 2; %s", code, out.String())
			}
			if !strings.Contains(out.String()+errb.String(), tc.want) {
				t.Fatalf("output %q lacks %q", out.String()+errb.String(), tc.want)
			}
		})
	}
}
