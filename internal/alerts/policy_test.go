package alerts

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"
)

// Política esperada da central, em cenários fictícios:
//   - alertas distintos chegam (nenhum se perde);
//   - repetições equivalentes respeitam dedup e cooldown;
//   - destino fora: o alerta fica pendente e é retentado;
//   - reinício: dedup, cooldown e pendências sobrevivem (com store);
//   - tenants não interferem entre si;
//   - volume limitado: muitos alertas não viram muitas mensagens.

type tenantSink struct {
	mu   sync.Mutex
	down map[string]bool
	got  []Notification
}

func (s *tenantSink) Deliver(_ context.Context, n Notification) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.down[n.Tenant] {
		return errors.New("destino do tenant fora (simulado)")
	}
	s.got = append(s.got, n)
	return nil
}

func covered(ns []Notification, tenant string) map[string]bool {
	m := map[string]bool{}
	for _, n := range ns {
		if n.Tenant != tenant {
			continue
		}
		for _, s := range n.Subjects {
			m[s] = true
		}
	}
	return m
}

func al(tenant, subject string, sev Severity) Alert {
	return Alert{Tenant: tenant, Source: "outbox-recovery", Kind: "entry_exhausted", Subject: subject, Severity: sev, Summary: "x " + subject}
}

func TestRestartKeepsDedupCooldownAndPending(t *testing.T) {
	clk := &clock{t: time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)}
	store := &MemoryStore{}
	sink := &SimulatedSink{}
	ctx := context.Background()

	c1 := New(cfg(), sink, clk.now)
	c1.SetStore(store)
	c1.Raise(ctx, exhausted("e1")) // entregue
	clk.add(time.Minute)
	c1.Raise(ctx, exhausted("e2")) // aguardando cooldown
	// processo morre aqui

	clk.add(time.Minute)
	c2 := New(cfg(), sink, clk.now)
	if err := c2.Restore(ctx, store); err != nil {
		t.Fatal(err)
	}
	c2.SetStore(store)
	if d, _ := c2.Raise(ctx, exhausted("e1")); d != Duplicate {
		t.Fatalf("dedup perdido no reinício: %s", d)
	}
	if d, _ := c2.Raise(ctx, exhausted("e3")); d != Deferred {
		t.Fatalf("cooldown perdido no reinício: %s", d)
	}
	if c2.Pending() != 2 {
		t.Fatalf("pendências perdidas: %d", c2.Pending())
	}
	clk.add(15 * time.Minute)
	c2.Flush(ctx)
	got := sink.Delivered()
	if len(got) != 2 || got[1].Alerts != 2 {
		t.Fatalf("depois do cooldown, e2+e3 numa notificação: %+v", got)
	}
}

func TestDestinationDownThenRestartThenRetry(t *testing.T) {
	clk := &clock{t: time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)}
	store := &MemoryStore{}
	sink := &SimulatedSink{FailNext: 1}
	ctx := context.Background()

	c1 := New(cfg(), sink, clk.now)
	c1.SetStore(store)
	if d, _ := c1.Raise(ctx, exhausted("e1")); d != DeliveryFailed {
		t.Fatalf("%s", d)
	}
	// reinício com o alerta ainda não entregue
	c2 := New(cfg(), sink, clk.now)
	if err := c2.Restore(ctx, store); err != nil {
		t.Fatal(err)
	}
	if err := c2.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	got := sink.Delivered()
	if len(got) != 1 || got[0].Subjects[0] != "e1" {
		t.Fatalf("alerta perdido no reinício: %+v", got)
	}
}

// Queda entre entregar e salvar: depois do reinício a notificação sai de novo.
// É o comportamento escolhido (pelo menos uma vez): repetir é melhor que perder.
func TestCrashBetweenDeliverAndSaveRepeatsNotLoses(t *testing.T) {
	clk := &clock{t: time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)}
	store := &MemoryStore{}
	sink := &SimulatedSink{}
	ctx := context.Background()
	c1 := New(cfg(), sink, clk.now)
	c1.SetStore(store)
	c1.Raise(ctx, exhausted("e0")) // estado salvo: e0 entregue
	clk.add(16 * time.Minute)
	sink.FailNext = 1
	c1.Raise(ctx, exhausted("e1")) // falha: e1 pendente e salvo
	store.FailSaves = 1            // a próxima gravação não chega ao store
	c1.Flush(ctx)                  // entrega e1, mas não grava: simula queda
	if c1.PersistErrors() != 1 {
		t.Fatal("falha de gravação não contada")
	}
	c2 := New(cfg(), sink, clk.now)
	c2.Restore(ctx, store)
	c2.Flush(ctx)
	n := 0
	for _, x := range sink.Delivered() {
		if len(x.Subjects) > 0 && x.Subjects[0] == "e1" {
			n++
		}
	}
	if n != 2 {
		t.Fatalf("esperava e1 entregue 2 vezes (pelo menos uma vez), veio %d", n)
	}
}

func TestTenantsAreIsolated(t *testing.T) {
	clk := &clock{t: time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)}
	sink := &tenantSink{down: map[string]bool{"clinica-b": true}}
	c := New(cfg(), sink, clk.now)
	ctx := context.Background()

	// mesmo assunto nos dois tenants: não é duplicata entre tenants
	if d, _ := c.Raise(ctx, al("clinica-a", "e1", Critical)); d != Delivered {
		t.Fatalf("a: %s", d)
	}
	if d, _ := c.Raise(ctx, al("clinica-b", "e1", Critical)); d != DeliveryFailed {
		t.Fatalf("b com destino fora: %s", d)
	}
	// destino de B fora não segura A; cooldown de A não segura B
	clk.add(time.Minute)
	if d, _ := c.Raise(ctx, al("clinica-a", "e2", Critical)); d != Deferred {
		t.Fatalf("a em cooldown: %s", d)
	}
	sink.down["clinica-b"] = false
	if d, _ := c.Raise(ctx, al("clinica-b", "e2", Critical)); d != Delivered {
		t.Fatalf("b sem cooldown (nunca entregou): %s", d)
	}
	for _, n := range sink.got {
		if n.Tenant == "" {
			t.Fatal("notificação sem tenant")
		}
	}
	if b := covered(sink.got, "clinica-b"); !b["e1"] || !b["e2"] {
		t.Fatalf("b: %v", b)
	}
	if a := covered(sink.got, "clinica-a"); !a["e1"] || a["e2"] {
		t.Fatalf("a: %v (e2 ainda no cooldown)", a)
	}
}

// Volume limitado: 1.000 eventos distintos em 1 h no mesmo grupo viram no
// máximo 1 + 60/15 = 5 notificações, e todos os 1.000 aparecem na contagem.
func TestVolumeIsBoundedAndNothingIsLost(t *testing.T) {
	clk := &clock{t: time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)}
	sink := &SimulatedSink{}
	c := New(Config{DedupWindow: time.Hour, Cooldown: 15 * time.Minute, MaxSubjects: 20}, sink, clk.now)
	ctx := context.Background()
	for i := 0; i < 1000; i++ {
		c.Raise(ctx, exhausted(fmt.Sprintf("e%04d", i)))
		clk.add(3600 * time.Millisecond)
		if i%50 == 0 {
			c.Flush(ctx)
		}
	}
	clk.add(15 * time.Minute)
	c.Flush(ctx)
	got := sink.Delivered()
	total := 0
	for _, n := range got {
		total += n.Alerts
	}
	if total != 1000 {
		t.Fatalf("eventos cobertos: %d", total)
	}
	if len(got) > 6 { // 5 na hora + o resumo final depois do último cooldown
		t.Fatalf("volume não limitado: %d notificações", len(got))
	}
}
