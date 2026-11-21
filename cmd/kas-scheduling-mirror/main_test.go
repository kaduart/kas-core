package main

import (
	"bytes"
	"strings"
	"testing"
)

func env(over map[string]string) func(string) string {
	base := map[string]string{
		"KAS_MIRROR_MONGO_URI": "mongodb://127.0.0.1:27217/?directConnection=true",
		"KAS_MIRROR_PG_URL":    "postgres://kas:x@127.0.0.1:55432/kas_core_test?sslmode=disable",
		"KAS_MIRROR_KEY":       strings.Repeat("k", 32),
	}
	for k, v := range over {
		base[k] = v
	}
	return func(k string) string { return base[k] }
}

func TestRefusesAnythingButLocalDatabases(t *testing.T) {
	ok := []string{"-mode", "full", "-tenant", "clinica-aurora", "-mongo-db", "fono_espelho"}
	cases := []struct {
		name string
		args []string
		env  map[string]string
		want string
	}{
		{"atlas srv", ok, map[string]string{"KAS_MIRROR_MONGO_URI": "mongodb+srv://u:p@cluster0.example.mongodb.net/"}, "plain mongodb"},
		{"remote mongo", ok, map[string]string{"KAS_MIRROR_MONGO_URI": "mongodb://10.0.0.5:27017/"}, "loopback"},
		{"one remote host in a list", ok, map[string]string{"KAS_MIRROR_MONGO_URI": "mongodb://127.0.0.1:27017,db.example.com:27017/"}, "loopback"},
		{"remote postgres", ok, map[string]string{"KAS_MIRROR_PG_URL": "postgres://u:p@db.example.com/kas"}, "loopback"},
		{"prod db name", []string{"-mode", "full", "-tenant", "clinica-aurora", "-mongo-db", "fono-prod"}, nil, "production"},
		{"short key", ok, map[string]string{"KAS_MIRROR_KEY": "curta"}, "32 bytes"},
		{"bad mode", []string{"-mode", "apagar", "-tenant", "clinica-aurora", "-mongo-db", "x"}, nil, "-mode"},
		{"window too big", []string{"-mode", "verify", "-tenant", "clinica-aurora", "-mongo-db", "x", "-from", "2027-01-01", "-to", "2029-01-01"}, nil, "400 days"},
		{"batch too big", []string{"-mode", "full", "-tenant", "clinica-aurora", "-mongo-db", "x", "-batch", "100000"}, nil, "-batch"},
	}
	for _, c := range cases {
		var out, errb bytes.Buffer
		if code := run(c.args, env(c.env), &out, &errb); code != 2 || !strings.Contains(errb.String(), c.want) {
			t.Errorf("%s: code %d, stderr %q", c.name, code, errb.String())
		}
		if strings.Contains(errb.String(), "u:p@") {
			t.Errorf("%s: credentials echoed", c.name)
		}
	}
}
