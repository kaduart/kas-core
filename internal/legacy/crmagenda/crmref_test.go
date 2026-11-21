package crmagenda

// Reference models of the CRM's scheduling rules, transcribed BY READING the
// CRM source (fono-inova-crm-back c1c5c4e, fono-inova-crm-front at the same
// day). They exist only to compare the KAS generic engine with what the CRM
// does today; nothing here runs against the CRM. Each function names the file
// and lines it mirrors. Only date logic is kept: queries, money and messages
// are left out (they are clinical rules, handled by the module, not the core).

import (
	"sort"
	"strings"
	"time"
)

var spLoc, _ = time.LoadLocation("America/Sao_Paulo")

func ymd(s string) time.Time {
	t, err := time.ParseInLocation("2006-01-02", s, spLoc)
	if err != nil {
		panic(err)
	}
	return t
}

func fmtDay(t time.Time) string { return t.Format("2006-01-02") }

// isoDay: Monday = 1 ... Sunday = 7 (date-fns getISODay).
func isoDay(t time.Time) int { return (int(t.Weekday())+6)%7 + 1 }

type slotOut struct{ date, time string }

func (s slotOut) String() string { return s.date + " " + s.time }

func outStrings(list []slotOut) []string {
	out := make([]string, len(list))
	for i, s := range list {
		out[i] = s.String()
	}
	sort.Strings(out)
	return out
}

// crmPackageForm mirrors the package form's schedule generator
// (front src/components/patients/TherapyPackageFormModal.tsx, ~lines 660-770):
// weeks counted from the ISO week (Monday) of the start date; weekly steps one
// week, biweekly two; days before the start in week 0 are left out; Saturday
// and Sunday are dropped silently. dayTimes maps ISO weekday to time; the
// start date's own weekday always takes startTime.
func crmPackageForm(start, startTime string, total int, dayTimes map[int]string, intervalWeeks int) []slotOut {
	st := ymd(start)
	active := []int{isoDay(st)}
	times := map[int]string{isoDay(st): startTime}
	for d, tm := range dayTimes {
		if d >= 1 && d <= 5 {
			if _, ok := times[d]; !ok {
				active = append(active, d)
			}
			times[d] = tm
		}
	}
	sort.Ints(active)
	week := st.AddDate(0, 0, -(isoDay(st) - 1))
	var out []slotOut
	for guard := 0; len(out) < total && guard < 600; guard++ {
		for _, d := range active {
			if len(out) >= total {
				break
			}
			day := week.AddDate(0, 0, d-1)
			if day.Before(st) || d > 5 {
				continue
			}
			out = append(out, slotOut{fmtDay(day), times[d]})
		}
		week = week.AddDate(0, 0, 7*intervalWeeks)
	}
	return out
}

// crmPackageFormSameDay mirrors the "sessions on the same day" branch of the
// same function: every listed time on the start date's weekday, in time order.
func crmPackageFormSameDay(start string, times []string, total, intervalWeeks int) []slotOut {
	st := ymd(start)
	sorted := append([]string(nil), times...)
	sort.Strings(sorted)
	week := st.AddDate(0, 0, -(isoDay(st) - 1))
	var out []slotOut
	for guard := 0; len(out) < total && guard < 600; guard++ {
		day := week.AddDate(0, 0, isoDay(st)-1)
		if !day.Before(st) && isoDay(st) <= 5 {
			for _, tm := range sorted {
				if len(out) >= total {
					break
				}
				out = append(out, slotOut{fmtDay(day), tm})
			}
		}
		week = week.AddDate(0, 0, 7*intervalWeeks)
	}
	return out
}

// crmSkipHolidays mirrors adjustScheduleSkipHolidays
// (back controllers/packageController.v2.js ~lines 108-170): keeps every
// non-holiday date; each holiday date jumps 7*intervalWeeks days at a time
// until a DATE not used yet (the time is not compared, and the new date is not
// checked against holidays).
func crmSkipHolidays(schedule []slotOut, intervalWeeks int, holidays map[string]bool) []slotOut {
	used := map[string]bool{}
	var out []slotOut
	for _, s := range schedule {
		if !holidays[s.date] {
			out = append(out, s)
			used[s.date] = true
		}
	}
	for _, s := range schedule {
		if !holidays[s.date] {
			continue
		}
		cur := ymd(s.date)
		var next string
		for attempts := 0; ; attempts++ {
			cur = cur.AddDate(0, 0, 7*intervalWeeks)
			next = fmtDay(cur)
			if !used[next] || attempts > 52 {
				break
			}
		}
		out = append(out, slotOut{next, s.time})
		used[next] = true
	}
	return out
}

// crmSlot is a weekly slot of an insurance plan or a court-order (liminar)
// contract: dayOfWeek 0 = Sunday, as JavaScript getDay.
type crmSlot struct {
	dow  int
	time string
}

// jsDateStr mirrors `date.toISOString().split('T')[0]` for a Date at local
// midnight on a server running in serverLoc.
func jsDateStr(day time.Time, serverLoc *time.Location) string {
	local := time.Date(day.Year(), day.Month(), day.Day(), 0, 0, 0, 0, serverLoc)
	return local.UTC().Format("2006-01-02")
}

// crmInsurancePlan mirrors generateInsurancePlanSessions
// (back services/schedule/generateInsurancePlanSessions.js ~lines 205-290):
// start = max(plan start, today); weeks from the Sunday of that week;
// weeksNeeded = ceil(remaining / slots) + 1; slots in weekday order; holidays,
// historical cancellations and evaluation slots are skipped (not moved).
func crmInsurancePlan(planStart, today string, remaining int, slots []crmSlot, holidays, canceled, evaluations map[string]bool, serverLoc *time.Location) []slotOut {
	start := ymd(planStart)
	if t := ymd(today); t.After(start) {
		start = t
	}
	ordered := append([]crmSlot(nil), slots...)
	sort.SliceStable(ordered, func(i, j int) bool { return ordered[i].dow < ordered[j].dow })
	weeksNeeded := (remaining+len(slots)-1)/len(slots) + 1
	weekStart := start.AddDate(0, 0, -int(start.Weekday()))
	var out []slotOut
	for w := 0; w < weeksNeeded && len(out) < remaining; w++ {
		sunday := weekStart.AddDate(0, 0, 7*w)
		for _, s := range ordered {
			if len(out) >= remaining {
				break
			}
			day := sunday.AddDate(0, 0, s.dow)
			date := jsDateStr(day, serverLoc)
			if day.Before(start) || holidays[date] || canceled[date+"T"+s.time] || evaluations[date+"T"+s.time] {
				continue
			}
			out = append(out, slotOut{date, s.time})
		}
	}
	return out
}

// crmInsuranceRemaining mirrors the balance of a guide (same file ~line 195):
// authorized - used - already reserved. A clinical rule: the module computes it.
func crmInsuranceRemaining(total, used, reserved int) int {
	if r := total - used - reserved; r > 0 {
		return r
	}
	return 0
}

// crmLiminar mirrors generateLiminarSessions
// (back services/schedule/generateLiminarSessions.js ~lines 117-165):
// minDate = max(plan start, max(requested start, today)); per slot, `weeks`
// non-holiday, non-canceled occurrences, stopping at lastDate; dates that
// already exist are counted but not created again.
func crmLiminar(planStart, today, requested string, weeks int, slots []crmSlot, holidays, canceled map[string]bool, lastDate string, existing map[string]bool) []slotOut {
	floor := ymd(today)
	if requested != "" {
		if r := ymd(requested); r.After(floor) {
			floor = r
		}
	}
	minDate := ymd(planStart)
	if floor.After(minDate) {
		minDate = floor
	}
	var out []slotOut
	for _, s := range slots {
		cand := minDate.AddDate(0, 0, -int(minDate.Weekday())+s.dow)
		for cand.Before(minDate) {
			cand = cand.AddDate(0, 0, 7)
		}
		for occ, guard := 0, 0; occ < weeks && guard < 260; guard++ {
			date := fmtDay(cand)
			if lastDate != "" && date > lastDate {
				break
			}
			if !holidays[date] && !canceled[date] {
				occ++
				if !existing[date+" "+s.time] {
					out = append(out, slotOut{date, s.time})
				}
			}
			cand = cand.AddDate(0, 0, 7)
		}
	}
	return out
}

// crmLiminarConflict mirrors the liminar conflict check (same file ~line 182):
// another active appointment of the doctor or patient on the SAME date and
// EXACT same time. A partial overlap is not seen.
func crmLiminarConflict(cand slotOut, existing []slotOut) bool {
	for _, e := range existing {
		if e.date == cand.date && e.time == cand.time {
			return true
		}
	}
	return false
}

type crmAppt struct {
	time     string
	duration int
	status   string
}

func clockMin(s string) int {
	var h, m int
	parts := strings.SplitN(s, ":", 2)
	for _, c := range parts[0] {
		h = h*10 + int(c-'0')
	}
	for _, c := range parts[1] {
		m = m*10 + int(c-'0')
	}
	return h*60 + m
}

// crmBlocking mirrors NON_BLOCKING_OPERATIONAL_STATUSES
// (back constants/appointmentStatus.js): only canceled and completed free a slot.
func crmBlocking(status string) bool {
	switch status {
	case "canceled", "cancelado", "cancelada", "completed":
		return false
	}
	return true
}

// crmAvailableTimes mirrors calculateAvailableSlots
// (back middleware/conflictDetection.js ~lines 541-640): the doctor's explicit
// start times of that weekday; a holiday blocks all day, or only the morning
// (< 12:00) / afternoon for partial holidays; a slot [t, t+40) is taken when
// any blocking appointment overlaps it. There is no buffer and no per-period
// vacation in this calculation (the doctor's status "ferias" is not read here).
func crmAvailableTimes(times []string, holiday string, appts []crmAppt) []string {
	var free []string
	for _, t := range times {
		start := clockMin(t)
		morning := start < 12*60
		if holiday == "full" || (holiday == "morning" && morning) || (holiday == "afternoon" && !morning) {
			continue
		}
		taken := false
		for _, a := range appts {
			if !crmBlocking(a.status) {
				continue
			}
			as := clockMin(a.time)
			d := a.duration
			if d == 0 {
				d = 40
			}
			if start < as+d && as < start+40 {
				taken = true
			}
		}
		if !taken {
			free = append(free, t)
		}
	}
	return free
}
