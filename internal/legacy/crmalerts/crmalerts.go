// Package crmalerts is the CRM adapter of the generic alert center: it turns
// what the outbox recovery and the event reaper observe on the Fono Inova CRM
// into alerts.Alert values, with CRM wording and CRM operating hours.
//
// The generic center (internal/alerts) knows nothing of this.
package crmalerts

import (
	"fmt"
	"time"

	"kas-core-pilot/internal/alerts"
	"kas-core-pilot/internal/eventreaper"
	"kas-core-pilot/internal/outboxrecovery"
)

const (
	SourceOutbox = "crm.outbox"
	SourceReaper = "crm.eventstore"
)

// Hours describes when the CRM worker (which runs the outbox dispatcher) is
// expected to be suspended. Outside it, pending entries piling up mean the
// dispatcher stopped; inside it, they are expected.
type Hours struct {
	Location            *time.Location
	SuspendFrom         int // minutes after midnight, e.g. 19*60+10
	ResumeAt            int // minutes after midnight, e.g. 7*60+30
	SuspendedOnWeekends bool
}

// FonoInovaHours: crm-worker suspended 19:10 to 07:30 on weekdays and all weekend
// (crons/workerScheduler.cron.js, America/Sao_Paulo).
func FonoInovaHours() Hours {
	loc, err := time.LoadLocation("America/Sao_Paulo")
	if err != nil {
		loc = time.FixedZone("BRT", -3*3600)
	}
	return Hours{Location: loc, SuspendFrom: 19*60 + 10, ResumeAt: 7*60 + 30, SuspendedOnWeekends: true}
}

// Suspended reports whether t falls in the expected suspension.
func (h Hours) Suspended(t time.Time) bool {
	if h.Location == nil {
		return false
	}
	l := t.In(h.Location)
	if h.SuspendedOnWeekends && (l.Weekday() == time.Saturday || l.Weekday() == time.Sunday) {
		return true
	}
	m := l.Hour()*60 + l.Minute()
	return m >= h.SuspendFrom || m < h.ResumeAt
}

// OutboxExhausted: the recovery gave up on an entry. Always critical: an event
// that will never be delivered is a business effect lost (payment, session).
func OutboxExhausted(a outboxrecovery.Alert) alerts.Alert {
	return alerts.Alert{
		Source: SourceOutbox, Kind: "entry_exhausted", Subject: a.ID, Severity: alerts.Critical,
		Summary: fmt.Sprintf("Evento do Outbox abandonado após as retentativas (%s). Precisa de decisão humana.", a.Reason),
	}
}

// StaleCriticalAt: from this many stale pending entries the alert is critical.
const StaleCriticalAt = 500

// OutboxCycle turns one recovery cycle into alerts (census only; exhausted
// entries arrive through OutboxExhausted).
func OutboxCycle(cy outboxrecovery.Cycle, h Hours) []alerts.Alert {
	var out []alerts.Alert
	if cy.Census != nil && cy.Census.PendingStale > 0 {
		sev := alerts.Warning
		note := "Nenhum dispatcher parece estar rodando."
		switch {
		case h.Suspended(cy.At):
			sev, note = alerts.Info, "Esperado: o crm-worker está no horário de suspensão."
		case cy.Census.PendingStale >= StaleCriticalAt:
			sev = alerts.Critical
		}
		out = append(out, alerts.Alert{
			Source: SourceOutbox, Kind: "pending_stale", Subject: "dispatcher", Severity: sev,
			Summary: fmt.Sprintf("%d eventos pendentes atrasados no Outbox. %s", cy.Census.PendingStale, note),
		})
	}
	if cy.AlertErrs > 0 {
		out = append(out, alerts.Alert{
			Source: SourceOutbox, Kind: "alert_sink_errors", Subject: "recovery", Severity: alerts.Warning,
			Summary: fmt.Sprintf("%d alertas da recuperação não foram registrados no ciclo.", cy.AlertErrs),
		})
	}
	return out
}

// ReaperCycle turns one reaper cycle into alerts.
func ReaperCycle(rep eventreaper.CycleReport) []alerts.Alert {
	var out []alerts.Alert
	if rep.Err != "" {
		out = append(out, alerts.Alert{
			Source: SourceReaper, Kind: "cycle_error", Subject: "cycle", Severity: alerts.Warning,
			Summary: "Ciclo do reaper do Event Store falhou: " + rep.Err,
		})
	}
	for _, c := range rep.Checks {
		if c.Verdict == "diverge" {
			out = append(out, alerts.Alert{
				Source: SourceReaper, Kind: "shadow_diverge", Subject: c.EventID, Severity: alerts.Critical,
				Summary: fmt.Sprintf("Sombra do reaper divergiu do Node: previsto %s, observado %s.", c.Predicted, c.Observed),
			})
		}
	}
	for _, o := range rep.Outcomes {
		if o.Result == "error" {
			out = append(out, alerts.Alert{
				Source: SourceReaper, Kind: "transition_error", Subject: o.EventID, Severity: alerts.Warning,
				Summary: "Transição do reaper falhou: " + o.Error,
			})
		}
	}
	return out
}
