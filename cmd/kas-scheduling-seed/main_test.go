package main

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"kas-core-pilot/internal/pgtest"
)

func TestRefusals(t *testing.T) {
	env := func(v string) func(string) string { return func(string) string { return v } }
	for _, c := range []struct {
		args []string
		dsn  string
		want string
	}{
		{[]string{"-tenant", "fono-inova"}, "postgres://u:p@127.0.0.1/kas", "not a fictitious tenant"},
		{nil, "postgres://u:p@db.exemplo.com/kas", "loopback"},
		{nil, "postgres://u:p@127.0.0.1/kas_prod", "production"},
		{nil, "mongodb://127.0.0.1/kas", "loopback"},
	} {
		var out, errb bytes.Buffer
		if code := run(c.args, env(c.dsn), time.Now, &out, &errb); code != 2 || !strings.Contains(errb.String(), c.want) {
			t.Fatalf("%v %s: code %d %q", c.args, c.dsn, code, errb.String())
		}
	}
}

func TestSeedIsIdempotent(t *testing.T) {
	dsn := pgtest.SchemaDSN(t)
	// Monday 08:30 local: today's 08:00 booking is in the past and skipped.
	now := func() time.Time { return time.Date(2027, 3, 1, 11, 30, 0, 0, time.UTC) }
	env := func(string) string { return dsn }
	var out, errb bytes.Buffer
	if code := run([]string{"-migrate", "-tenant", "odonto-sorriso"}, env, now, &out, &errb); code != 0 {
		t.Fatalf("first run: %s", errb.String())
	}
	first := out.String()
	out.Reset()
	if code := run([]string{"-tenant", "odonto-sorriso"}, env, now, &out, &errb); code != 0 {
		t.Fatalf("second run: %s", errb.String())
	}
	if strings.Contains(first, " 0 agendamentos novos") || !strings.Contains(out.String(), " 0 agendamentos novos") {
		t.Fatalf("first %q second %q", first, out.String())
	}
}
