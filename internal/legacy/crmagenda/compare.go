package crmagenda

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"kas-core-pilot/internal/scheduling"
)

// Comparison engine (phase 3A): for every professional and local day of a
// window, compares what the CRM would show (its own slot rule over its own
// appointments) with what the KAS computes over the mirror, and says why they
// differ:
//
//	equal            same bookings, same free times
//	import_error     the mirror does not hold what the source holds (missing,
//	                 extra or stale row, professional not mirrored); the
//	                 availability difference is a consequence, not a rule
//	clinical_rule    same bookings, different free times, explained by a known
//	                 rule (holiday calendar, KAS exception such as vacation,
//	                 professional status)
//	real_divergence  same bookings, different free times, no known reason
//
// The report carries opaque ids only. Each run's counts go to the history.

// Kinds of a resource-day.
const (
	KindEqual    = "equal"
	KindImport   = "import_error"
	KindClinical = "clinical_rule"
	KindReal     = "real_divergence"
)

// MaxCompareDays bounds a comparison window.
const MaxCompareDays = 93

// DayReport is one professional on one local day.
type DayReport struct {
	ResourceID     string         `json:"resource_id"`
	Day            string         `json:"day"`
	Kind           string         `json:"kind"`
	Reasons        []string       `json:"reasons"`
	SourceByStatus map[string]int `json:"source_by_status"`
	MirrorByStatus map[string]int `json:"mirror_by_status"`
	CRMFree        []string       `json:"crm_free"`
	KASFree        []string       `json:"kas_free"`
	SourceOverlaps int            `json:"source_overlaps"`
}

// ComparisonReport is the result of Compare.
type ComparisonReport struct {
	Tenant        string                      `json:"tenant_id"`
	Summary       scheduling.ComparisonCounts `json:"summary"`
	Professionals int                         `json:"professionals"`
	SourceRows    int                         `json:"source_rows"`
	Skipped       Skips                       `json:"skipped"`
	ByStatus      map[string]map[string]int   `json:"by_status"` // CRM status -> KAS status -> count
	Days          []DayReport                 `json:"days"`      // non-equal days, up to the limit
	ListedLimit   int                         `json:"listed_limit"`
}

func sameStrings(a, b []string) bool {
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

// Compare runs the comparison over local days [from, to] (YYYY-MM-DD,
// inclusive) and records its counts in the history.
func (im *Importer) Compare(ctx context.Context, from, to string, maxListed int) (ComparisonReport, error) {
	rep := ComparisonReport{Tenant: im.Tenant, Skipped: Skips{}, ByStatus: map[string]map[string]int{}, Days: []DayReport{}, ListedLimit: maxListed}
	if err := im.check(); err != nil {
		return rep, err
	}
	loc := im.Mapper.loc
	f, err1 := time.ParseInLocation("2006-01-02", from, loc)
	t, err2 := time.ParseInLocation("2006-01-02", to, loc)
	if err1 != nil || err2 != nil || t.Before(f) || t.Sub(f) > MaxCompareDays*24*time.Hour || maxListed < 0 {
		return rep, scheduling.ErrInvalid
	}
	lo, hi := f, t.AddDate(0, 0, 1)
	docs, err := im.Reader.InWindow(ctx, lo, hi)
	if err != nil {
		return rep, fmt.Errorf("read source: %w", err)
	}
	doctors, err := im.Reader.Doctors(ctx)
	if err != nil {
		return rep, fmt.Errorf("read source: %w", err)
	}
	rep.SourceRows, rep.Professionals = len(docs), len(doctors)
	settings, err := im.Store.GetSettings(ctx, im.Tenant)
	if errors.Is(err, scheduling.ErrNotFound) {
		settings, err = scheduling.DefaultSettings(), nil
	}
	if err != nil {
		return rep, err
	}
	kasHoliday := map[string]bool{}
	for _, h := range settings.Holidays {
		kasHoliday[h] = true
	}
	type rkey struct{ res, day string }
	src := map[rkey][]scheduling.MirrorItem{}
	for _, it := range im.mapAll(docs, rep.Skipped) {
		if it.Start.Before(lo) || !it.Start.Before(hi) {
			continue
		}
		src[rkey{it.ResourceID, it.Start.In(loc).Format("2006-01-02")}] = append(src[rkey{it.ResourceID, it.Start.In(loc).Format("2006-01-02")}], it)
	}
	pattern := func(m map[string]int, res string, s time.Time) {
		l := s.In(loc)
		m[fmt.Sprintf("%s|%d|%s", res, l.Weekday(), l.Format("15:04"))]++
	}
	srcPatterns, mirPatterns := map[string]int{}, map[string]int{}
	counts := &rep.Summary
	counts.WindowFrom, counts.WindowTo = from, to
	for _, d := range doctors {
		res := im.Mapper.Ref("pr", d.ID.Hex())
		times := d.StartTimes()
		mirrored, err := im.Store.ListBookings(ctx, im.Tenant, res, lo, hi)
		if err != nil {
			return rep, err
		}
		mir := map[string][]scheduling.Booking{}
		for _, b := range mirrored {
			mir[b.Start.In(loc).Format("2006-01-02")] = append(mir[b.Start.In(loc).Format("2006-01-02")], b)
		}
		exceptions, err := im.Store.ListExceptions(ctx, im.Tenant, res, lo, hi)
		if err != nil {
			return rep, err
		}
		for day := f; !day.After(t); day = day.AddDate(0, 0, 1) {
			ds := day.Format("2006-01-02")
			srcItems, mirItems := src[rkey{res, ds}], mir[ds]
			dayTimes := times[int(day.Weekday())]
			if len(dayTimes) == 0 && len(srcItems) == 0 && len(mirItems) == 0 {
				continue
			}
			dr := DayReport{ResourceID: res, Day: ds, Reasons: []string{}, SourceByStatus: map[string]int{}, MirrorByStatus: map[string]int{}}
			// 1. Bookings: does the mirror hold exactly the source's rows?
			byID := map[string]scheduling.Booking{}
			for _, b := range mirItems {
				byID[b.ID] = b
				dr.MirrorByStatus[b.Status]++
				if scheduling.ActiveStatus(b.Status) {
					pattern(mirPatterns, res, b.Start)
				}
			}
			var busy []scheduling.Interval
			missing, changed := 0, 0
			for i, it := range srcItems {
				dr.SourceByStatus[it.SourceStatus]++
				if rep.ByStatus[it.SourceStatus] == nil {
					rep.ByStatus[it.SourceStatus] = map[string]int{}
				}
				rep.ByStatus[it.SourceStatus][it.Status]++
				if scheduling.ActiveStatus(it.Status) {
					busy = append(busy, scheduling.Interval{Start: it.Start, End: it.End})
					pattern(srcPatterns, res, it.Start)
					for _, o := range srcItems[i+1:] {
						if scheduling.ActiveStatus(o.Status) && o.Start.Before(it.End) && it.Start.Before(o.End) {
							dr.SourceOverlaps++
						}
					}
				}
				b, ok := byID[it.BookingID]
				switch {
				case !ok:
					missing++
				case !b.Start.Equal(it.Start) || !b.End.Equal(it.End) || b.Status != it.Status:
					changed++
				}
				delete(byID, it.BookingID)
			}
			if missing > 0 {
				dr.Reasons = append(dr.Reasons, fmt.Sprintf("missing_in_mirror:%d", missing))
			}
			if changed > 0 {
				dr.Reasons = append(dr.Reasons, fmt.Sprintf("stale_in_mirror:%d", changed))
			}
			if len(byID) > 0 {
				dr.Reasons = append(dr.Reasons, fmt.Sprintf("extra_in_mirror:%d", len(byID)))
			}
			// 2. Free times on both sides.
			dr.CRMFree = CRMFreeTimes(dayTimes, CRMHoliday(ds), busy, day)
			slots, err := im.Store.Availability(ctx, im.Tenant, scheduling.AvailabilityQuery{ResourceID: res, DurationMin: SlotMinutes, From: ds, To: ds})
			switch {
			case errors.Is(err, scheduling.ErrNotFound):
				dr.Reasons = append(dr.Reasons, "professional_not_mirrored")
			case err != nil:
				return rep, err
			}
			dr.KASFree = []string{}
			for _, s := range slots {
				dr.KASFree = append(dr.KASFree, s.Start.In(loc).Format("15:04"))
			}
			sort.Strings(dr.KASFree)
			// 3. Classify.
			switch {
			case len(dr.Reasons) > 0:
				dr.Kind = KindImport
			case sameStrings(dr.CRMFree, dr.KASFree):
				dr.Kind = KindEqual
			default:
				crmHol := CRMHoliday(ds)
				if (crmHol == "full") != kasHoliday[ds] || crmHol == "morning" {
					dr.Reasons = append(dr.Reasons, "holiday_calendar")
				}
				for _, e := range exceptions {
					if e.Start.Before(day.AddDate(0, 0, 1)) && e.End.After(day) {
						dr.Reasons = append(dr.Reasons, "kas_exception")
						break
					}
				}
				if d.Status == "ferias" || d.Status == "afastado" || d.Status == "inativo" {
					dr.Reasons = append(dr.Reasons, "professional_status_"+d.Status)
				}
				dr.Kind = KindReal
				if len(dr.Reasons) > 0 {
					dr.Kind = KindClinical
				}
			}
			counts.ResourceDays++
			switch dr.Kind {
			case KindEqual:
				counts.Equal++
			case KindImport:
				counts.ImportErrors++
			case KindClinical:
				counts.ClinicalRules++
			default:
				counts.RealDivergence++
			}
			if dr.Kind != KindEqual && len(rep.Days) < maxListed {
				rep.Days = append(rep.Days, dr)
			}
		}
	}
	// Recurrence: the weekly patterns (professional, weekday, time) of active
	// bookings, with their counts, must be the same on both sides.
	keys := map[string]bool{}
	for k := range srcPatterns {
		keys[k] = true
	}
	for k := range mirPatterns {
		keys[k] = true
	}
	for k := range keys {
		counts.PatternsTotal++
		if srcPatterns[k] == mirPatterns[k] {
			counts.PatternsEqual++
		}
	}
	id, at, err := im.Store.RecordComparison(ctx, im.Tenant, Source, *counts)
	if err != nil {
		return rep, err
	}
	counts.ID, counts.CreatedAt = id, at
	im.Log.Info("mirror_compare", "tenant_id", im.Tenant, "resource_days", counts.ResourceDays, "equal", counts.Equal,
		"import_errors", counts.ImportErrors, "clinical_rules", counts.ClinicalRules, "real_divergence", counts.RealDivergence,
		"patterns_equal", counts.PatternsEqual, "patterns_total", counts.PatternsTotal)
	return rep, nil
}

var kindLabel = map[string]string{KindEqual: "igual", KindImport: "erro de importação", KindClinical: "regra clínica", KindReal: "divergência real"}

// Markdown renders a comparison for people (opaque ids only).
func (rep ComparisonReport) Markdown() string {
	var b strings.Builder
	s := rep.Summary
	fmt.Fprintf(&b, "# Comparação KAS × CRM — %s a %s\n\n", s.WindowFrom, s.WindowTo)
	fmt.Fprintf(&b, "Tenant `%s` · comparação nº %d · %d profissionais · %d linhas da origem\n\n", rep.Tenant, s.ID, rep.Professionals, rep.SourceRows)
	fmt.Fprintf(&b, "| Resultado | Profissional-dia |\n|---|---|\n| igual | %d |\n| erro de importação | %d |\n| regra clínica | %d |\n| divergência real | %d |\n| **total** | **%d** |\n\n",
		s.Equal, s.ImportErrors, s.ClinicalRules, s.RealDivergence, s.ResourceDays)
	fmt.Fprintf(&b, "Padrões semanais (profissional, dia da semana, horário) iguais: %d de %d.\n\n", s.PatternsEqual, s.PatternsTotal)
	if len(rep.Skipped) > 0 {
		b.WriteString("Linhas da origem ignoradas: ")
		keys := make([]string, 0, len(rep.Skipped))
		for k := range rep.Skipped {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for i, k := range keys {
			if i > 0 {
				b.WriteString(", ")
			}
			fmt.Fprintf(&b, "%s %d", k, rep.Skipped[k])
		}
		b.WriteString(".\n\n")
	}
	b.WriteString("## Status (CRM → KAS)\n\n| CRM | KAS | Qtde |\n|---|---|---|\n")
	crm := make([]string, 0, len(rep.ByStatus))
	for k := range rep.ByStatus {
		crm = append(crm, k)
	}
	sort.Strings(crm)
	for _, c := range crm {
		kas := make([]string, 0, len(rep.ByStatus[c]))
		for k := range rep.ByStatus[c] {
			kas = append(kas, k)
		}
		sort.Strings(kas)
		for _, k := range kas {
			fmt.Fprintf(&b, "| %s | %s | %d |\n", c, k, rep.ByStatus[c][k])
		}
	}
	if len(rep.Days) > 0 {
		fmt.Fprintf(&b, "\n## Dias que não bateram (até %d)\n\n| Dia | Profissional | Resultado | Motivos | Livres no CRM | Livres no KAS |\n|---|---|---|---|---|---|\n", rep.ListedLimit)
		for _, d := range rep.Days {
			fmt.Fprintf(&b, "| %s | `%s` | %s | %s | %s | %s |\n", d.Day, d.ResourceID, kindLabel[d.Kind], strings.Join(d.Reasons, ", "),
				strings.Join(d.CRMFree, " "), strings.Join(d.KASFree, " "))
		}
	}
	return b.String()
}
