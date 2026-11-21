package crmagenda

import (
	"context"
	"fmt"
	"sort"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"kas-core-pilot/internal/scheduling"
)

// The CRM's own slot rules, ported so the KAS can be compared with what the
// CRM shows (phase 3A). Sources, read only:
//   back middleware/conflictDetection.js   calculateAvailableSlots, BUSINESS_START/END
//   back config/feriadosBR-dynamic.js      fixed and Easter-based holidays, partial days
//   back models/Doctor.js                  weeklyAvailability [{day, times[]}], status

// DoctorCollection is the CRM's professionals collection.
const DoctorCollection = "doctors"

// DoctorProjection: only the agenda fields of a professional. No name, email,
// phone, council number or any other field is read.
var DoctorProjection = bson.D{{Key: "_id", Value: 1}, {Key: "weeklyAvailability", Value: 1}, {Key: "status", Value: 1}}

// DoctorDoc is one professional as read.
type DoctorDoc struct {
	ID     bson.ObjectID `bson:"_id"`
	Weekly []struct {
		Day   string   `bson:"day"`
		Times []string `bson:"times"`
	} `bson:"weeklyAvailability"`
	Status string `bson:"status"`
}

// SlotMinutes is the length the CRM gives every slot of its availability list.
const SlotMinutes = 40

var dayKeys = map[string]int{"sunday": 0, "monday": 1, "tuesday": 2, "wednesday": 3, "thursday": 4, "friday": 5, "saturday": 6}

// StartTimes gives, per weekday (0 = Sunday), the CRM's normalized start times:
// valid HH:MM, inside 08:00-18:00, deduplicated and sorted.
func (d DoctorDoc) StartTimes() map[int][]string {
	out := map[int][]string{}
	for _, w := range d.Weekly {
		wd, ok := dayKeys[w.Day]
		if !ok {
			continue
		}
		seen := map[string]bool{}
		for _, t := range w.Times {
			p := clock.FindStringSubmatch(t)
			if p == nil {
				continue
			}
			hh := p[1]
			if len(hh) == 1 {
				hh = "0" + hh
			}
			norm := hh + ":" + p[2]
			if norm < "08:00" || norm > "18:00" || seen[norm] {
				continue
			}
			seen[norm] = true
			out[wd] = append(out[wd], norm)
		}
		sort.Strings(out[wd])
	}
	return out
}

// Doctors reads every professional (agenda fields only).
func (r *Reader) Doctors(ctx context.Context) ([]DoctorDoc, error) {
	cur, err := r.coll.Database().Collection(DoctorCollection).Find(ctx, bson.D{}, options.Find().SetProjection(DoctorProjection))
	if err != nil {
		return nil, err
	}
	var out []DoctorDoc
	if err := cur.All(ctx, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func easter(year int) time.Time {
	a, b, c := year%19, year/100, year%100
	d, e := b/4, b%4
	f := (b + 8) / 25
	g := (b - f + 1) / 3
	h := (19*a + b - d - g + 15) % 30
	i, k := c/4, c%4
	l := (32 + 2*e + 2*i - h - k) % 7
	m := (a + 11*h + 22*l) / 451
	month := (h + l - 7*m + 114) / 31
	day := (h+l-7*m+114)%31 + 1
	return time.Date(year, time.Month(month), day, 12, 0, 0, 0, time.UTC)
}

var fixedHolidays = map[string]bool{"01-01": true, "04-21": true, "05-01": true, "09-07": true, "10-12": true, "11-02": true, "11-15": true, "12-25": true}

// CRMHoliday returns "full", "morning" or "" for a local date, by the CRM's
// calendar (which, as read, has no 20/11).
func CRMHoliday(date string) string {
	t, err := time.Parse("2006-01-02", date)
	if err != nil {
		return ""
	}
	if fixedHolidays[date[5:]] {
		return "full"
	}
	e := easter(t.Year())
	switch date {
	case e.AddDate(0, 0, -48).Format("2006-01-02"), e.AddDate(0, 0, -2).Format("2006-01-02"), e.AddDate(0, 0, 60).Format("2006-01-02"):
		return "full"
	case e.AddDate(0, 0, -47).Format("2006-01-02"):
		return "morning"
	}
	return ""
}

// CRMFreeTimes is calculateAvailableSlots without shadow patterns and without
// the Session collection: a start time is free unless the day (or that half
// of it) is a holiday or an active appointment overlaps [t, t+40).
func CRMFreeTimes(times []string, holiday string, busy []scheduling.Interval, day time.Time) []string {
	free := []string{}
	for _, t := range times {
		var h, m int
		fmt.Sscanf(t, "%d:%d", &h, &m)
		if holiday == "full" || (holiday == "morning" && h < 12) {
			continue
		}
		s := time.Date(day.Year(), day.Month(), day.Day(), h, m, 0, 0, day.Location())
		slot := scheduling.Interval{Start: s, End: s.Add(SlotMinutes * time.Minute)}
		taken := false
		for _, b := range busy {
			if slot.Overlaps(b) {
				taken = true
				break
			}
		}
		if !taken {
			free = append(free, t)
		}
	}
	return free
}

// SyncAvailability copies every professional's weekly start times into the
// KAS as weekly rules of its mirrored resource: one 40-minute window per CRM
// start time. Returns how many professionals were synced.
func (im *Importer) SyncAvailability(ctx context.Context) (int, error) {
	if err := im.check(); err != nil {
		return 0, err
	}
	docs, err := im.Reader.Doctors(ctx)
	if err != nil {
		return 0, fmt.Errorf("read source: %w", err)
	}
	n := 0
	for _, d := range docs {
		res := im.Mapper.Ref("pr", d.ID.Hex())
		var rules []scheduling.WeeklyRule
		for wd, times := range d.StartTimes() {
			for _, t := range times {
				var h, m int
				fmt.Sscanf(t, "%d:%d", &h, &m)
				rules = append(rules, scheduling.WeeklyRule{Weekday: wd, StartMin: h*60 + m, EndMin: h*60 + m + SlotMinutes})
			}
		}
		if len(rules) > 100 {
			return n, fmt.Errorf("%w: a professional has more than 100 weekly start times", scheduling.ErrInvalid)
		}
		if err := im.Store.PutResource(ctx, im.Tenant, scheduling.Resource{ID: res, Kind: "person", Label: res, Capacity: 1, Active: true}); err != nil {
			return n, err
		}
		if err := im.Store.PutWeeklyRules(ctx, im.Tenant, res, rules); err != nil {
			return n, err
		}
		n++
	}
	im.Log.Info("mirror_availability", "tenant_id", im.Tenant, "professionals", n)
	return n, nil
}
