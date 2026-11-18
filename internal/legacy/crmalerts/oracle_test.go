package crmalerts

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"kas-core-pilot/internal/alerts"
	"kas-core-pilot/internal/outboxrecovery"
)

// Comparação Node x Go no MESMO cenário fictício: o alertService atual do CRM
// (sem modificação, via legacy-oracle/alerts) contra a central de alertas Go.
//
//	KAS_TEST_ORACLE_ALERTS_DIR=$PWD/legacy-oracle/alerts go test ./internal/legacy/crmalerts -run Oracle -v

type step struct {
	Min      int
	Subject  string // "" = só flush
	SinkDown bool
}

// Cinco eventos do Outbox abandonados, uma repetição e uma queda do webhook.
var scenario = []step{
	{0, "A", false}, {1, "B", false}, {2, "A", false}, {3, "C", false},
	{20, "", false},
	{36, "D", true}, {37, "E", false},
	{60, "", false},
}

type toggleSink struct {
	down bool
	got  []alerts.Notification
}

func (s *toggleSink) Deliver(_ context.Context, n alerts.Notification) error {
	if s.down {
		return errors.New("webhook 503 (simulado)")
	}
	s.got = append(s.got, n)
	return nil
}

func TestOracleNodeVsGoSameScenario(t *testing.T) {
	dir := os.Getenv("KAS_TEST_ORACLE_ALERTS_DIR")
	if dir == "" {
		t.Skip("KAS_TEST_ORACLE_ALERTS_DIR is not set; skipping the Node alerts oracle")
	}
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node is not installed")
	}
	base := time.Date(2026, 11, 18, 13, 0, 0, 0, time.UTC) // quarta 10:00 em Brasília

	// Node
	cmd := exec.Command("node", filepath.Join(dir, "oracle.js"))
	cmd.Stderr = os.Stderr
	in, _ := cmd.StdinPipe()
	outPipe, _ := cmd.StdoutPipe()
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = in.Close(); _ = cmd.Wait() }()
	sc := bufio.NewScanner(outPipe)
	sc.Buffer(make([]byte, 1<<20), 1<<24)
	call := func(v any, out any) {
		b, _ := json.Marshal(v)
		if _, err := io.WriteString(in, string(b)+"\n"); err != nil {
			t.Fatal(err)
		}
		if !sc.Scan() {
			t.Fatalf("oracle closed: %v", sc.Err())
		}
		if err := json.Unmarshal(sc.Bytes(), out); err != nil {
			t.Fatalf("oracle %q: %v", sc.Text(), err)
		}
	}

	// Go
	now := base
	sink := &toggleSink{}
	center := alerts.New(alerts.DefaultConfig(), sink, func() time.Time { return now })
	ctx := context.Background()

	var trace []string
	for _, s := range scenario {
		now = base.Add(time.Duration(s.Min) * time.Minute)
		sink.down = s.SinkDown
		if s.Subject == "" {
			_ = center.Flush(ctx)
			trace = append(trace, fmt.Sprintf("%02dmin flush         | Node: (nada) | Go: %d notificações até aqui", s.Min, len(sink.got)))
			continue
		}
		var node struct {
			Sent      bool
			Reason    *string
			Delivered bool
		}
		call(map[string]any{"cmd": "raise", "nowMs": now.UnixMilli(), "level": "critical", "type": "outbox_entry_exhausted",
			"message": "Evento do Outbox abandonado", "details": map[string]string{"id": s.Subject}, "sinkDown": s.SinkDown}, &node)
		d, _ := center.Raise(ctx, OutboxExhausted(outboxrecovery.Alert{ID: s.Subject, Reason: "max_attempts"}))
		nodeSays := "entregue"
		switch {
		case !node.Sent:
			nodeSays = "descartado (" + *node.Reason + ")"
		case !node.Delivered:
			nodeSays = "marcado como enviado, webhook falhou: perdido"
		}
		trace = append(trace, fmt.Sprintf("%02dmin alerta %s%s | Node: %s | Go: %s", s.Min, s.Subject, map[bool]string{true: " (webhook fora)", false: ""}[s.SinkDown], nodeSays, d))
	}

	var delivered struct {
		Payloads []struct{ Details map[string]string }
	}
	call(map[string]any{"cmd": "delivered"}, &delivered)
	nodeCovered := map[string]bool{}
	for _, p := range delivered.Payloads {
		nodeCovered[p.Details["id"]] = true
	}
	goCovered := map[string]bool{}
	for _, n := range sink.got {
		for _, s := range n.Subjects {
			goCovered[s] = true
		}
	}
	keys := func(m map[string]bool) []string {
		var out []string
		for k := range m {
			out = append(out, k)
		}
		sort.Strings(out)
		return out
	}
	t.Logf("\n%s\nNode: %d notificações, eventos avisados %v\nGo:   %d notificações, eventos avisados %v",
		strings.Join(trace, "\n"), len(delivered.Payloads), keys(nodeCovered), len(sink.got), keys(goCovered))

	if got := keys(goCovered); strings.Join(got, ",") != "A,B,C,D,E" {
		t.Fatalf("Go precisa avisar os 5 eventos, avisou %v", got)
	}
	if len(sink.got) != 3 {
		t.Fatalf("Go: esperava 3 notificações (A; B+C; D+E), veio %d", len(sink.got))
	}
	// Comportamento atual do Node, fixado pelo hash do arquivo copiado:
	// chave = tipo+nível, cooldown gravado antes do envio.
	if got := keys(nodeCovered); strings.Join(got, ",") != "A" {
		t.Fatalf("o comportamento do Node mudou: avisou %v", got)
	}
}
