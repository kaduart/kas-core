package crmagenda

import (
	"fmt"
	"os"
	"sort"
	"strings"
	"testing"
	"time"

	"kas-core-pilot/internal/scheduling"
)

// Equivalence (phase 2): the same fictitious situation is run through the
// CRM's rules (crmref_test.go, transcribed by reading) and through the KAS
// generic engine. Each scenario declares what is expected:
//
//	igual            same dates and times
//	regra_clinica    the difference is a clinical/business rule; the health
//	                 module computes it and hands the generic engine its input
//	                 (count, dates to skip); the test checks that, given that
//	                 input, both agree
//	defeito_crm      the CRM gives a wrong answer by its own intent; the KAS
//	                 is deliberately different (nothing is changed in the CRM)
//	diferenca_kas    the KAS has a concept the CRM lacks (buffer, vacations
//	                 by period); with it switched off both agree
//
// The table is also written to docs when KAS_EQUIV_REPORT names a file.

type verdict string

const (
	vEqual    verdict = "igual"
	vClinical verdict = "regra_clinica"
	vCRMBug   verdict = "defeito_crm"
	vKASExtra verdict = "diferenca_kas"
)

type scenario struct {
	id, area, title string
	want            verdict
	note            string
	run             func(t *testing.T) (crm, kas []string)
}

func kasSeries(t *testing.T, rule scheduling.SeriesRule, holidays ...string) []string {
	t.Helper()
	st := scheduling.DefaultSettings()
	st.Holidays = append([]string{}, holidays...)
	if rule.DurationMin == 0 {
		rule.DurationMin = 40
	}
	if rule.HolidayPolicy == "" {
		rule.HolidayPolicy = scheduling.HolidaySkip
	}
	res, err := scheduling.ExpandSeries(rule, st)
	if err != nil {
		t.Fatalf("ExpandSeries: %v", err)
	}
	out := make([]string, len(res.Occurrences))
	for i, o := range res.Occurrences {
		out[i] = o.Start.In(spLoc).Format("2006-01-02 15:04")
	}
	sort.Strings(out)
	return out
}

func set(items ...string) map[string]bool {
	m := map[string]bool{}
	for _, i := range items {
		m[i] = true
	}
	return m
}

func merge(lists ...[]string) []string {
	var out []string
	for _, l := range lists {
		out = append(out, l...)
	}
	sort.Strings(out)
	return out
}

// kasFree lists the free starts of one resource-day in the KAS: the doctor's
// explicit start times become one 40-minute window each.
func kasFree(t *testing.T, day string, times []string, closed []scheduling.Interval, bookings []scheduling.Booking, bufferMin int) []string {
	t.Helper()
	wd := int(ymd(day).Weekday())
	var rules []scheduling.WeeklyRule
	for _, tm := range times {
		m := clockMin(tm)
		rules = append(rules, scheduling.WeeklyRule{Weekday: wd, StartMin: m, EndMin: m + 40 + bufferMin})
	}
	var exc []scheduling.Exception
	for _, c := range closed {
		exc = append(exc, scheduling.Exception{Start: c.Start, End: c.End, Reason: "fechado"})
	}
	slots, err := scheduling.Availability(scheduling.AvailabilityInput{Settings: scheduling.DefaultSettings(),
		Resource: scheduling.Resource{ID: "pr-1", Capacity: 1, Active: true}, Rules: rules, Exceptions: exc, Bookings: bookings,
		From: day, To: day, DurationMin: 40, BufferMin: bufferMin})
	if err != nil {
		t.Fatalf("Availability: %v", err)
	}
	out := make([]string, len(slots))
	for i, s := range slots {
		out[i] = s.Start.In(spLoc).Format("15:04")
	}
	return out
}

func at(day, clock string) time.Time {
	t, err := time.ParseInLocation("2006-01-02 15:04", day+" "+clock, spLoc)
	if err != nil {
		panic(err)
	}
	return t
}

func booking(id, day, clock string, minutes int, status string) scheduling.Booking {
	s := at(day, clock)
	return scheduling.Booking{ID: id, ResourceID: "pr-1", ClientRef: "cl-" + id, Start: s, End: s.Add(time.Duration(minutes) * time.Minute),
		BlockedUntil: s.Add(time.Duration(minutes) * time.Minute), Status: status}
}

// 2027-03-01 is a Monday. Fictitious holidays are passed explicitly to both sides.
func scenarios() []scenario {
	pkg := func(start, tm string, total int, days map[int]string, interval int, holidays ...string) []string {
		return outStrings(crmSkipHolidays(crmPackageForm(start, tm, total, days, interval), interval, set(holidays...)))
	}
	return []scenario{
		// ---------- Pacotes (formulário do front + ajuste de feriados do back) ----------
		{"R01", "recorrência", "pacote semanal, 1 dia, 8 sessões", vEqual, "",
			func(t *testing.T) ([]string, []string) {
				return pkg("2027-03-01", "08:00", 8, nil, 1),
					kasSeries(t, scheduling.SeriesRule{Frequency: "weekly", Weekdays: []int{1}, Time: "08:00", StartDate: "2027-03-01", Count: 8})
			}},
		{"R02", "recorrência", "pacote semanal, seg 08:00 e qua 14:00, início na segunda", vEqual, "horário por dia (time_by_weekday)",
			func(t *testing.T) ([]string, []string) {
				return pkg("2027-03-01", "08:00", 7, map[int]string{3: "14:00"}, 1),
					kasSeries(t, scheduling.SeriesRule{Frequency: "weekly", Weekdays: []int{1, 3}, Time: "08:00", TimeByWeekday: map[int]string{3: "14:00"}, StartDate: "2027-03-01", Count: 7})
			}},
		{"R03", "recorrência", "pacote semanal seg+qua começando numa quarta", vEqual, "a segunda da semana 0 fica fora",
			func(t *testing.T) ([]string, []string) {
				return pkg("2027-03-03", "14:00", 5, map[int]string{1: "08:00"}, 1),
					kasSeries(t, scheduling.SeriesRule{Frequency: "weekly", Weekdays: []int{1, 3}, Time: "14:00", TimeByWeekday: map[int]string{1: "08:00"}, StartDate: "2027-03-03", Count: 5})
			}},
		{"R04", "recorrência", "pacote quinzenal, 1 dia, 6 sessões", vEqual, "",
			func(t *testing.T) ([]string, []string) {
				return pkg("2027-03-02", "10:00", 6, nil, 2),
					kasSeries(t, scheduling.SeriesRule{Frequency: "biweekly", Weekdays: []int{2}, Time: "10:00", StartDate: "2027-03-02", Count: 6})
			}},
		{"R05", "recorrência", "pacote quinzenal seg+qua começando numa quarta", vEqual,
			"corrigido no KAS nesta fase: antes a segunda caía na semana ímpar; agora os dois dias ficam na mesma quinzena, como no CRM",
			func(t *testing.T) ([]string, []string) {
				return pkg("2027-03-03", "14:00", 6, map[int]string{1: "08:00"}, 2),
					kasSeries(t, scheduling.SeriesRule{Frequency: "biweekly", Weekdays: []int{1, 3}, Time: "14:00", TimeByWeekday: map[int]string{1: "08:00"}, StartDate: "2027-03-03", Count: 6})
			}},
		{"R06", "feriados", "pacote semanal com 1 feriado no meio", vEqual, "CRM empurra para depois do fim; KAS pula e estende: mesmas datas",
			func(t *testing.T) ([]string, []string) {
				return pkg("2027-03-01", "09:00", 6, nil, 1, "2027-03-15"),
					kasSeries(t, scheduling.SeriesRule{Frequency: "weekly", Weekdays: []int{1}, Time: "09:00", StartDate: "2027-03-01", Count: 6}, "2027-03-15")
			}},
		{"R07", "feriados", "pacote: data empurrada cai em outro feriado", vCRMBug,
			"CRM não confere feriado na data nova e marca sessão no feriado; KAS pula também esse",
			func(t *testing.T) ([]string, []string) {
				return pkg("2027-03-01", "09:00", 4, nil, 1, "2027-03-08", "2027-03-29"),
					kasSeries(t, scheduling.SeriesRule{Frequency: "weekly", Weekdays: []int{1}, Time: "09:00", StartDate: "2027-03-01", Count: 4}, "2027-03-08", "2027-03-29")
			}},
		{"R08", "feriados", "pacote quinzenal com feriado", vEqual, "salto de 14 dias nos dois",
			func(t *testing.T) ([]string, []string) {
				return pkg("2027-03-02", "10:00", 4, nil, 2, "2027-03-16"),
					kasSeries(t, scheduling.SeriesRule{Frequency: "biweekly", Weekdays: []int{2}, Time: "10:00", StartDate: "2027-03-02", Count: 4}, "2027-03-16")
			}},
		{"R09", "virada", "pacote semanal atravessando mês e ano", vEqual, "",
			func(t *testing.T) ([]string, []string) {
				return pkg("2026-12-21", "15:00", 5, nil, 1),
					kasSeries(t, scheduling.SeriesRule{Frequency: "weekly", Weekdays: []int{1}, Time: "15:00", StartDate: "2026-12-21", Count: 5})
			}},
		{"R10", "virada", "sextas com Natal e Ano Novo seguidos", vCRMBug,
			"25/12 é empurrado para 01/01, outro feriado; KAS pula os dois",
			func(t *testing.T) ([]string, []string) {
				return pkg("2026-12-18", "16:00", 4, nil, 1, "2026-12-25", "2027-01-01"),
					kasSeries(t, scheduling.SeriesRule{Frequency: "weekly", Weekdays: []int{5}, Time: "16:00", StartDate: "2026-12-18", Count: 4}, "2026-12-25", "2027-01-01")
			}},
		{"R11", "disponibilidade semanal", "pacote com sábado escolhido", vClinical,
			"o formulário do CRM descarta sábado e domingo em silêncio (a clínica não abre); no KAS quem fecha é a regra semanal da clínica: o módulo só manda dias úteis",
			func(t *testing.T) ([]string, []string) {
				return pkg("2027-03-01", "08:00", 4, map[int]string{6: "09:00"}, 1),
					kasSeries(t, scheduling.SeriesRule{Frequency: "weekly", Weekdays: []int{1}, Time: "08:00", StartDate: "2027-03-01", Count: 4})
			}},
		{"R12", "recorrência", "pacote com 2 sessões no mesmo dia, total ímpar", vEqual,
			"KAS: duas séries (08:00 com 3, 09:00 com 2)",
			func(t *testing.T) ([]string, []string) {
				crm := outStrings(crmSkipHolidays(crmPackageFormSameDay("2027-03-01", []string{"09:00", "08:00"}, 5, 1), 1, set()))
				kas := merge(
					kasSeries(t, scheduling.SeriesRule{Frequency: "weekly", Weekdays: []int{1}, Time: "08:00", StartDate: "2027-03-01", Count: 3}),
					kasSeries(t, scheduling.SeriesRule{Frequency: "weekly", Weekdays: []int{1}, Time: "09:00", StartDate: "2027-03-01", Count: 2}))
				return crm, kas
			}},
		{"R13", "feriados", "2 sessões no mesmo dia caindo num feriado", vCRMBug,
			"o CRM compara só a data: a 08:00 vai para a data livre e a 09:00 é empurrada mais uma semana; KAS mantém as duas juntas",
			func(t *testing.T) ([]string, []string) {
				crm := outStrings(crmSkipHolidays(crmPackageFormSameDay("2027-03-01", []string{"08:00", "09:00"}, 6, 1), 1, set("2027-03-08")))
				kas := merge(
					kasSeries(t, scheduling.SeriesRule{Frequency: "weekly", Weekdays: []int{1}, Time: "08:00", StartDate: "2027-03-01", Count: 3}, "2027-03-08"),
					kasSeries(t, scheduling.SeriesRule{Frequency: "weekly", Weekdays: []int{1}, Time: "09:00", StartDate: "2027-03-01", Count: 3}, "2027-03-08"))
				return crm, kas
			}},
		// ---------- Plano de convênio ----------
		{"I14", "recorrência", "convênio, 1 horário semanal, saldo 6", vEqual, "início = maior entre início do plano e hoje",
			func(t *testing.T) ([]string, []string) {
				crm := outStrings(crmInsurancePlan("2027-02-15", "2027-03-03", 6, []crmSlot{{4, "10:00"}}, set(), set(), set(), spLoc))
				return crm, kasSeries(t, scheduling.SeriesRule{Frequency: "weekly", Weekdays: []int{4}, Time: "10:00", StartDate: "2027-03-03", Count: 6})
			}},
		{"I15", "recorrência", "convênio, sexta e quarta cadastradas fora de ordem, saldo ímpar", vEqual, "o corte do saldo cai no último dia da semana",
			func(t *testing.T) ([]string, []string) {
				crm := outStrings(crmInsurancePlan("2027-03-01", "2027-03-01", 5, []crmSlot{{5, "11:00"}, {3, "09:00"}}, set(), set(), set(), spLoc))
				return crm, kasSeries(t, scheduling.SeriesRule{Frequency: "weekly", Weekdays: []int{3, 5}, Time: "09:00", TimeByWeekday: map[int]string{5: "11:00"}, StartDate: "2027-03-01", Count: 5})
			}},
		{"I16", "feriados", "convênio com 1 feriado", vEqual, "",
			func(t *testing.T) ([]string, []string) {
				crm := outStrings(crmInsurancePlan("2027-03-01", "2027-03-01", 4, []crmSlot{{1, "08:00"}}, set("2027-03-08"), set(), set(), spLoc))
				return crm, kasSeries(t, scheduling.SeriesRule{Frequency: "weekly", Weekdays: []int{1}, Time: "08:00", StartDate: "2027-03-01", Count: 4}, "2027-03-08")
			}},
		{"I17", "feriados", "convênio, 1 horário, 2 feriados no período", vCRMBug,
			"a folga de 1 semana (weeksNeeded + 1) não cobre 2 feriados: o CRM gera 3 de 4 sessões autorizadas; KAS gera as 4",
			func(t *testing.T) ([]string, []string) {
				crm := outStrings(crmInsurancePlan("2027-03-01", "2027-03-01", 4, []crmSlot{{1, "08:00"}}, set("2027-03-08", "2027-03-15"), set(), set(), spLoc))
				return crm, kasSeries(t, scheduling.SeriesRule{Frequency: "weekly", Weekdays: []int{1}, Time: "08:00", StartDate: "2027-03-01", Count: 4}, "2027-03-08", "2027-03-15")
			}},
		{"I18", "estados internos", "convênio pula um cancelamento histórico (avaliação em outro horário não pula)", vClinical,
			"o módulo saúde escolhe as datas a pular (skip_dates); o motor genérico só pula",
			func(t *testing.T) ([]string, []string) {
				crm := outStrings(crmInsurancePlan("2027-03-01", "2027-03-01", 4, []crmSlot{{1, "08:00"}}, set(),
					set("2027-03-08T08:00"), set("2027-03-15T10:00"), spLoc))
				return crm, kasSeries(t, scheduling.SeriesRule{Frequency: "weekly", Weekdays: []int{1}, Time: "08:00", StartDate: "2027-03-01", Count: 4,
					SkipDates: []string{"2027-03-08"}})
			}},
		{"I19", "estados internos", "saldo da guia (10 autorizadas, 3 usadas, 2 reservadas)", vClinical,
			"saldo é regra do convênio: o módulo calcula 5 e passa count=5",
			func(t *testing.T) ([]string, []string) {
				rem := crmInsuranceRemaining(10, 3, 2)
				crm := outStrings(crmInsurancePlan("2027-03-01", "2027-03-01", rem, []crmSlot{{2, "13:00"}}, set(), set(), set(), spLoc))
				return crm, kasSeries(t, scheduling.SeriesRule{Frequency: "weekly", Weekdays: []int{2}, Time: "13:00", StartDate: "2027-03-01", Count: rem})
			}},
		{"I20", "fuso horário", "convênio gerado num servidor com fuso positivo", vCRMBug,
			"toISOString de meia-noite local volta um dia em fuso UTC+; o KAS usa o fuso da clínica, não o do servidor",
			func(t *testing.T) ([]string, []string) {
				tokyo, _ := time.LoadLocation("Asia/Tokyo")
				crm := outStrings(crmInsurancePlan("2027-03-01", "2027-03-01", 3, []crmSlot{{1, "08:00"}}, set(), set(), set(), tokyo))
				return crm, kasSeries(t, scheduling.SeriesRule{Frequency: "weekly", Weekdays: []int{1}, Time: "08:00", StartDate: "2027-03-01", Count: 3})
			}},
		// ---------- Contrato de liminar ----------
		{"L21", "recorrência", "liminar, 2 horários, 4 semanas cada", vEqual, "KAS: uma série por horário",
			func(t *testing.T) ([]string, []string) {
				crm := outStrings(crmLiminar("2027-03-01", "2027-03-01", "", 4, []crmSlot{{1, "08:00"}, {4, "15:00"}}, set(), set(), "", set()))
				kas := merge(
					kasSeries(t, scheduling.SeriesRule{Frequency: "weekly", Weekdays: []int{1}, Time: "08:00", StartDate: "2027-03-01", Count: 4}),
					kasSeries(t, scheduling.SeriesRule{Frequency: "weekly", Weekdays: []int{4}, Time: "15:00", StartDate: "2027-03-01", Count: 4}))
				return crm, kas
			}},
		{"L22", "feriados", "liminar com feriado e cancelamento", vEqual, "os dois pulam e estendem",
			func(t *testing.T) ([]string, []string) {
				crm := outStrings(crmLiminar("2027-03-01", "2027-03-01", "", 4, []crmSlot{{1, "08:00"}}, set("2027-03-08"), set("2027-03-15"), "", set()))
				return crm, kasSeries(t, scheduling.SeriesRule{Frequency: "weekly", Weekdays: []int{1}, Time: "08:00", StartDate: "2027-03-01", Count: 4,
					SkipDates: []string{"2027-03-15"}}, "2027-03-08")
			}},
		{"L23", "recorrência", "liminar que vence antes de completar as semanas", vEqual, "KAS: count e until juntos (o que vier primeiro)",
			func(t *testing.T) ([]string, []string) {
				crm := outStrings(crmLiminar("2027-03-01", "2027-03-01", "", 8, []crmSlot{{3, "10:00"}}, set(), set(), "2027-03-24", set()))
				return crm, kasSeries(t, scheduling.SeriesRule{Frequency: "weekly", Weekdays: []int{3}, Time: "10:00", StartDate: "2027-03-01", Count: 8, Until: "2027-03-24"})
			}},
		{"L24", "cancelamento e remarcação", "liminar regerada não duplica o que já existe", vClinical,
			"no KAS a não duplicação é da gravação (chave de idempotência por ocorrência, fase 4A); a lista calculada é a mesma",
			func(t *testing.T) ([]string, []string) {
				crm := outStrings(crmLiminar("2027-03-01", "2027-03-01", "", 3, []crmSlot{{1, "08:00"}}, set(), set(), "", set("2027-03-08 08:00")))
				all := kasSeries(t, scheduling.SeriesRule{Frequency: "weekly", Weekdays: []int{1}, Time: "08:00", StartDate: "2027-03-01", Count: 3})
				var kas []string
				for _, s := range all {
					if s != "2027-03-08 08:00" {
						kas = append(kas, s)
					}
				}
				return crm, kas
			}},
		{"L25", "conflito de recurso", "liminar 08:20 contra sessão 08:00-08:40 do mesmo profissional", vCRMBug,
			"o CRM compara só a hora exata e não vê o choque; o KAS vê sobreposição parcial",
			func(t *testing.T) ([]string, []string) {
				crmSees := crmLiminarConflict(slotOut{"2027-03-01", "08:20"}, []slotOut{{"2027-03-01", "08:00"}})
				c, err := scheduling.FindConflicts([]scheduling.Candidate{{Key: "c1", ResourceID: "pr-1", ClientRef: "cl-novo",
					Start: at("2027-03-01", "08:20"), End: at("2027-03-01", "09:00")}},
					[]scheduling.Booking{booking("b1", "2027-03-01", "08:00", 40, scheduling.StatusScheduled)}, nil, true)
				if err != nil {
					t.Fatal(err)
				}
				return []string{fmt.Sprint("conflito=", crmSees)}, []string{fmt.Sprint("conflito=", len(c) > 0)}
			}},
		{"L26", "recorrência", "liminar com início pedido no futuro e plano antigo", vEqual, "início = maior entre plano, hoje e o pedido",
			func(t *testing.T) ([]string, []string) {
				crm := outStrings(crmLiminar("2027-01-04", "2027-03-01", "2027-03-10", 3, []crmSlot{{1, "08:00"}}, set(), set(), "", set()))
				return crm, kasSeries(t, scheduling.SeriesRule{Frequency: "weekly", Weekdays: []int{1}, Time: "08:00", StartDate: "2027-03-10", Count: 3})
			}},
		// ---------- Disponibilidade, conflitos, estados ----------
		{"A27", "disponibilidade semanal", "horários do profissional com um ocupado", vEqual, "",
			func(t *testing.T) ([]string, []string) {
				times := []string{"08:00", "08:40", "09:20", "10:00"}
				crm := crmAvailableTimes(times, "", []crmAppt{{"08:40", 40, "scheduled"}})
				return crm, kasFree(t, "2027-03-01", times, nil, []scheduling.Booking{booking("b1", "2027-03-01", "08:40", 40, scheduling.StatusScheduled)}, 0)
			}},
		{"A28", "estados internos", "pré-agendado e falta bloqueiam; cancelado e concluído liberam", vEqual, "pelo mapeamento de status do importador",
			func(t *testing.T) ([]string, []string) {
				times := []string{"08:00", "08:40", "09:20", "10:00"}
				appts := []crmAppt{{"08:00", 40, "pre_agendado"}, {"08:40", 40, "canceled"}, {"09:20", 40, "missed"}, {"10:00", 40, "completed"}}
				var bs []scheduling.Booking
				for i, a := range appts {
					bs = append(bs, booking(fmt.Sprintf("b%d", i), "2027-03-01", a.time, a.duration, KASStatus(a.status, false)))
				}
				return crmAvailableTimes(times, "", appts), kasFree(t, "2027-03-01", times, nil, bs, 0)
			}},
		{"A29", "feriados", "feriado parcial (Quarta de Cinzas, só manhã)", vEqual, "no KAS é uma exceção de meio período",
			func(t *testing.T) ([]string, []string) {
				times := []string{"08:00", "10:00", "13:00", "15:00"}
				closed := []scheduling.Interval{{Start: at("2027-02-10", "00:00"), End: at("2027-02-10", "12:00")}}
				return crmAvailableTimes(times, "morning", nil), kasFree(t, "2027-02-10", times, closed, nil, 0)
			}},
		{"A30", "férias e folgas", "férias do profissional numa semana", vKASExtra,
			"no cálculo de horários do CRM não há férias por período (o status \"ferias\" do profissional não é lido ali); o KAS fecha o período por exceção",
			func(t *testing.T) ([]string, []string) {
				times := []string{"08:00", "09:00"}
				closed := []scheduling.Interval{{Start: at("2027-03-01", "00:00"), End: at("2027-03-06", "00:00")}}
				return crmAvailableTimes(times, "", nil), kasFree(t, "2027-03-03", times, closed, nil, 0)
			}},
		{"A31", "intervalos", "intervalo de 10 min depois da sessão", vKASExtra,
			"o CRM não tem intervalo; no KAS é configurado por serviço (com 0, os dois coincidem: ver A27)",
			func(t *testing.T) ([]string, []string) {
				times := []string{"08:00", "08:40", "09:30"}
				bs := []scheduling.Booking{booking("b1", "2027-03-01", "08:00", 40, scheduling.StatusScheduled)}
				bs[0].BlockedUntil = bs[0].End.Add(10 * time.Minute)
				bs[0].BufferMin = 10
				return crmAvailableTimes(times, "", []crmAppt{{"08:00", 40, "scheduled"}}), kasFree(t, "2027-03-01", times, nil, bs, 10)
			}},
		{"A32", "conflito de cliente", "paciente já tem sessão com outro profissional no mesmo horário", vEqual,
			"convênio confere sobreposição do paciente; KAS com client_exclusive",
			func(t *testing.T) ([]string, []string) {
				// CRM insurance check (checkSlotConflicts): overlap of the patient's appointments.
				crmSees := clockMin("10:20") < clockMin("10:00")+40 && clockMin("10:00") < clockMin("10:20")+40
				other := booking("b1", "2027-03-01", "10:00", 40, scheduling.StatusScheduled)
				other.ResourceID, other.ClientRef = "pr-2", "cl-mesmo"
				c, err := scheduling.FindConflicts([]scheduling.Candidate{{Key: "c1", ResourceID: "pr-1", ClientRef: "cl-mesmo",
					Start: at("2027-03-01", "10:20"), End: at("2027-03-01", "11:00")}}, []scheduling.Booking{other}, nil, true)
				if err != nil {
					t.Fatal(err)
				}
				return []string{fmt.Sprint("conflito=", crmSees)}, []string{fmt.Sprint("conflito=", len(c) > 0 && c[0].Kind == "client")}
			}},
	}
}

func equalList(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestEquivalenceWithCRMRules(t *testing.T) {
	var report strings.Builder
	report.WriteString("| # | Área | Cenário | Resultado | CRM | KAS | Observação |\n|---|---|---|---|---|---|---|\n")
	counts := map[verdict]int{}
	list := scenarios()
	if len(list) < 30 {
		t.Fatalf("only %d scenarios", len(list))
	}
	for _, sc := range list {
		t.Run(sc.id, func(t *testing.T) {
			crm, kas := sc.run(t)
			same := equalList(crm, kas)
			switch sc.want {
			case vEqual, vClinical:
				if !same {
					t.Fatalf("%s: want the same answer\nCRM %v\nKAS %v", sc.title, crm, kas)
				}
			case vCRMBug, vKASExtra:
				if same {
					t.Fatalf("%s: a documented difference disappeared (update the scenario)\n%v", sc.title, crm)
				}
			}
			counts[sc.want]++
			fmt.Fprintf(&report, "| %s | %s | %s | %s | %s | %s | %s |\n", sc.id, sc.area, sc.title, sc.want,
				strings.Join(crm, ", "), strings.Join(kas, ", "), sc.note)
		})
	}
	fmt.Fprintf(&report, "\nTotais: igual %d · regra clínica %d · defeito do CRM %d · conceito extra do KAS %d\n",
		counts[vEqual], counts[vClinical], counts[vCRMBug], counts[vKASExtra])
	if path := os.Getenv("KAS_EQUIV_REPORT"); path != "" {
		if err := os.WriteFile(path, []byte(report.String()), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}
