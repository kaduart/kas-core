package scheduling

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// Scenarios C1–C11 of the Agenda spec, against a real PostgreSQL. Every request
// runs in its own goroutine with its own connection, released together by a
// barrier, so the database (not the Go code) decides who wins.

type outcome struct {
	ok, slotTaken, clientBusy, versionConflict, other atomic.Int64
	firstOther                                        atomic.Value
}

func (o *outcome) add(err error) {
	switch {
	case err == nil:
		o.ok.Add(1)
	case errors.Is(err, ErrSlotTaken):
		o.slotTaken.Add(1)
	case errors.Is(err, ErrClientBusy):
		o.clientBusy.Add(1)
	case errors.Is(err, ErrVersionConflict):
		o.versionConflict.Add(1)
	default:
		o.other.Add(1)
		o.firstOther.CompareAndSwap(nil, err.Error())
	}
}

// race runs n functions at the same instant and waits for all of them.
func race(n int, fn func(i int) error, out *outcome) {
	var start, done sync.WaitGroup
	start.Add(1)
	done.Add(n)
	for i := 0; i < n; i++ {
		go func(i int) {
			defer done.Done()
			start.Wait()
			out.add(fn(i))
		}(i)
	}
	start.Done()
	done.Wait()
}

func activeIn(t *testing.T, s *Store, tenant, resource string, from, to time.Time) int {
	return countRows(t, s, `SELECT count(*) FROM sched_bookings WHERE tenant_id = $1 AND resource_id = $2
		AND status IN ('tentative','scheduled','confirmed') AND starts_at < $4 AND ends_at > $3`, tenant, resource, from, to)
}

func orphanLogs(t *testing.T, s *Store) int {
	// A booking without its create log, or a log without its booking.
	return countRows(t, s, `SELECT
		(SELECT count(*) FROM sched_bookings b WHERE NOT EXISTS (SELECT 1 FROM sched_booking_log l
			WHERE l.tenant_id = b.tenant_id AND l.booking_id = b.id AND l.action = 'create')) +
		(SELECT count(*) FROM sched_booking_log l WHERE NOT EXISTS (SELECT 1 FROM sched_bookings b
			WHERE b.tenant_id = l.tenant_id AND b.id = l.booking_id))`)
}

// C1: 50 simultaneous bookings of the same slot, 20 rounds. One wins, 49 get
// slot_taken, no orphan. Round 1 holds the winner's transaction open for a
// moment and counts, in pg_stat_activity, the requests waiting on its lock.
func TestC01FiftySimultaneousBookings(t *testing.T) {
	s, dsn := openStore(t)
	seed(t, s, tenantA)
	widePool(t, s, dsn, 52) // 50 racers + the observer query, under max_connections in CI
	ctx := context.Background()
	const n, rounds = 50, 20
	var waiting atomic.Int64
	var hold sync.Once
	s.hook = func(stage string) error {
		if stage == "after_insert" {
			hold.Do(func() {
				time.Sleep(400 * time.Millisecond)
			})
		}
		return nil
	}
	started := time.Now()
	for r := 0; r < rounds; r++ {
		day := time.Date(2027, 2, 1, 0, 0, 0, 0, sp).AddDate(0, 0, 7*(r/5)+r%5) // weekdays only
		from := day.Add(9 * time.Hour)
		to := from.Add(40 * time.Minute)
		if r == 0 {
			go func() {
				time.Sleep(250 * time.Millisecond)
				var w int64
				_ = s.pool.QueryRow(ctx, `SELECT count(*) FROM pg_stat_activity
					WHERE datname = current_database() AND wait_event_type = 'Lock'`).Scan(&w)
				waiting.Store(w)
			}()
		}
		var out outcome
		race(n, func(i int) error {
			_, _, err := s.CreateBooking(ctx, tenantA, NewBooking{ResourceID: "sala-1", ClientRef: fmt.Sprintf("cli-%d-%d", r, i),
				IdempotencyKey: fmt.Sprintf("c1-%d-%d", r, i), Actor: "teste", Start: from, End: to})
			return err
		}, &out)
		if out.ok.Load() != 1 || out.slotTaken.Load() != n-1 || out.other.Load()+out.clientBusy.Load() != 0 {
			t.Fatalf("round %d: ok=%d slot_taken=%d other=%d (%v)", r+1, out.ok.Load(), out.slotTaken.Load(), out.other.Load(), out.firstOther.Load())
		}
		if got := activeIn(t, s, tenantA, "sala-1", from, to); got != 1 {
			t.Fatalf("round %d: %d active bookings in the slot", r+1, got)
		}
	}
	if o := orphanLogs(t, s); o != 0 {
		t.Fatalf("%d orphan rows", o)
	}
	total := countRows(t, s, `SELECT count(*) FROM sched_bookings`)
	logs := countRows(t, s, `SELECT count(*) FROM sched_booking_log`)
	t.Logf("C1 evidence: %d rounds x %d requests, %d bookings, %d log rows, %d requests waiting on the winner's lock in round 1, pool %d connections, %v total",
		rounds, n, total, logs, waiting.Load(), s.pool.Stat().TotalConns(), time.Since(started).Round(time.Millisecond))
	if total != rounds || logs != rounds {
		t.Fatalf("expected %d bookings and logs, got %d and %d", rounds, total, logs)
	}
	if waiting.Load() < 40 {
		t.Fatalf("only %d requests were waiting at the same time; the race was not real", waiting.Load())
	}
}

// C2: partial overlap and an interval contained in another are both refused.
func TestC02PartialAndContainedOverlap(t *testing.T) {
	s, _ := openStore(t)
	seed(t, s, tenantA)
	ctx := context.Background()
	for i, c := range []struct{ first, second [2]string }{
		{[2]string{"09:00", "09:40"}, [2]string{"09:20", "10:00"}},
		{[2]string{"14:00", "15:00"}, [2]string{"14:10", "14:30"}},
		{[2]string{"16:10", "16:30"}, [2]string{"16:00", "17:00"}}, // the new one contains the old
	} {
		d := "2027-01-05 "
		if _, _, err := s.CreateBooking(ctx, tenantA, book("sala-1", fmt.Sprintf("a%d", i), fmt.Sprintf("k%da", i), d+c.first[0], d+c.first[1])); err != nil {
			t.Fatal(err)
		}
		_, _, err := s.CreateBooking(ctx, tenantA, book("sala-1", fmt.Sprintf("b%d", i), fmt.Sprintf("k%db", i), d+c.second[0], d+c.second[1]))
		if !errors.Is(err, ErrSlotTaken) {
			t.Fatalf("case %d: %v", i, err)
		}
	}
	// The same client cannot be in two resources at once.
	if _, _, err := s.CreateBooking(ctx, tenantA, book("grupo-1", "a0", "kc", "2027-01-05 09:30", "2027-01-05 10:30")); !errors.Is(err, ErrClientBusy) {
		t.Fatalf("client overlap: %v", err)
	}
}

// C3: touching intervals ('[)') are both accepted.
func TestC03TouchingIntervals(t *testing.T) {
	s, _ := openStore(t)
	seed(t, s, tenantA)
	ctx := context.Background()
	for i, iv := range [][2]string{{"09:00", "09:40"}, {"09:40", "10:20"}, {"08:20", "09:00"}} {
		if _, _, err := s.CreateBooking(ctx, tenantA, book("sala-1", fmt.Sprintf("c%d", i), fmt.Sprintf("k%d", i),
			"2027-01-05 "+iv[0], "2027-01-05 "+iv[1])); err != nil {
			t.Fatalf("%v: %v", iv, err)
		}
	}
	// The same client back to back is also fine.
	if _, _, err := s.CreateBooking(ctx, tenantA, book("grupo-1", "c0", "k9", "2027-01-05 09:40", "2027-01-05 10:00")); err != nil {
		t.Fatal(err)
	}
}

// C4: two people move the same booking to different times at once, 10 rounds.
func TestC04ConcurrentReschedule(t *testing.T) {
	s, dsn := openStore(t)
	seed(t, s, tenantA)
	widePool(t, s, dsn, 10)
	ctx := context.Background()
	for r := 0; r < 10; r++ {
		day := fmt.Sprintf("2027-01-%02d ", 11+r%5)
		b, _, err := s.CreateBooking(ctx, tenantA, book("sala-1", fmt.Sprintf("c%d", r), fmt.Sprintf("k%d", r), day+fmt.Sprintf("%02d:00", 8+r/5), day+fmt.Sprintf("%02d:40", 8+r/5)))
		if err != nil {
			t.Fatal(err)
		}
		targets := []time.Time{at(day + "15:00"), at(day + "16:00")}
		if r >= 5 {
			targets = []time.Time{at(day + "13:00"), at(day + "17:00")}
		}
		var out outcome
		race(2, func(i int) error {
			_, err := s.Reschedule(ctx, tenantA, b.ID, 1, targets[i], targets[i].Add(40*time.Minute), "teste")
			return err
		}, &out)
		if out.ok.Load() != 1 || out.versionConflict.Load() != 1 {
			t.Fatalf("round %d: ok=%d version_conflict=%d other=%d", r, out.ok.Load(), out.versionConflict.Load(), out.other.Load())
		}
		var version, moves int
		_ = s.pool.QueryRow(ctx, `SELECT version FROM sched_bookings WHERE tenant_id = $1 AND id = $2`, tenantA, b.ID).Scan(&version)
		moves = countRows(t, s, `SELECT count(*) FROM sched_booking_log WHERE tenant_id = $1 AND booking_id = $2 AND action = 'reschedule'`, tenantA, b.ID)
		if version != 2 || moves != 1 {
			t.Fatalf("round %d: version %d, %d moves logged", r, version, moves)
		}
	}
}

// C5: A moves to 14:00 while B books 14:00, 10 rounds. Only one holds 14:00; if
// A loses, A keeps its old time; if A wins, its old time is free again.
func TestC05RescheduleAgainstNewBooking(t *testing.T) {
	s, dsn := openStore(t)
	seed(t, s, tenantA)
	widePool(t, s, dsn, 10)
	ctx := context.Background()
	wins := map[string]int{}
	for r := 0; r < 10; r++ {
		day := fmt.Sprintf("2027-01-%02d ", 18+r%5)
		hour := 8 + r/5
		old := [2]time.Time{at(day + fmt.Sprintf("%02d:00", hour)), at(day + fmt.Sprintf("%02d:40", hour))}
		target := at(day + fmt.Sprintf("%02d:00", 14+r/5))
		a, _, err := s.CreateBooking(ctx, tenantA, NewBooking{ResourceID: "sala-1", ClientRef: fmt.Sprintf("a%d", r), IdempotencyKey: fmt.Sprintf("ka%d", r),
			Actor: "teste", Start: old[0], End: old[1]})
		if err != nil {
			t.Fatal(err)
		}
		// Odd rounds: A's move holds its transaction open for 150 ms and B arrives
		// 30 ms later, so B waits on A's uncommitted row (A must win). Even rounds
		// are a free race (the shorter insert usually wins).
		s.hook = nil
		if r%2 == 1 {
			s.hook = func(stage string) error {
				if stage == "after_update" {
					time.Sleep(150 * time.Millisecond)
				}
				return nil
			}
		}
		var moveErr, bookErr error
		var out outcome
		race(2, func(i int) error {
			if i == 0 {
				_, moveErr = s.Reschedule(ctx, tenantA, a.ID, 1, target, target.Add(40*time.Minute), "teste")
				return moveErr
			}
			if r%2 == 1 {
				time.Sleep(30 * time.Millisecond)
			}
			_, _, bookErr = s.CreateBooking(ctx, tenantA, NewBooking{ResourceID: "sala-1", ClientRef: fmt.Sprintf("b%d", r),
				IdempotencyKey: fmt.Sprintf("kb%d", r), Actor: "teste", Start: target, End: target.Add(40 * time.Minute)})
			return bookErr
		}, &out)
		if out.ok.Load() != 1 || out.slotTaken.Load() != 1 {
			t.Fatalf("round %d: move=%v book=%v", r, moveErr, bookErr)
		}
		if n := activeIn(t, s, tenantA, "sala-1", target, target.Add(40*time.Minute)); n != 1 {
			t.Fatalf("round %d: %d bookings at the target", r, n)
		}
		oldTaken := activeIn(t, s, tenantA, "sala-1", old[0], old[1])
		if moveErr == nil {
			wins["move"]++
			if oldTaken != 0 {
				t.Fatalf("round %d: A moved but its old slot is still held", r)
			}
		} else {
			wins["new booking"]++
			if oldTaken != 1 {
				t.Fatalf("round %d: A lost the race but lost its old slot too", r)
			}
		}
	}
	if wins["move"] == 0 || wins["new booking"] == 0 {
		t.Fatalf("both outcomes must be exercised: %v", wins)
	}
	t.Logf("C5 evidence: winners over 10 rounds %v", wins)
}

// C6: cancel and a new booking of the same slot at the same instant, 20 rounds.
// Never two active bookings.
func TestC06CancelAndBookSameInstant(t *testing.T) {
	s, dsn := openStore(t)
	seed(t, s, tenantA)
	widePool(t, s, dsn, 10)
	ctx := context.Background()
	results := map[string]int{}
	for r := 0; r < 20; r++ {
		day := time.Date(2027, 3, 1, 0, 0, 0, 0, sp).AddDate(0, 0, 7*(r/5)+r%5)
		from, to := day.Add(15*time.Hour), day.Add(15*time.Hour+40*time.Minute)
		b, _, err := s.CreateBooking(ctx, tenantA, NewBooking{ResourceID: "sala-1", ClientRef: fmt.Sprintf("a%d", r), IdempotencyKey: fmt.Sprintf("ka%d", r),
			Actor: "teste", Start: from, End: to})
		if err != nil {
			t.Fatal(err)
		}
		var bookErr error
		var out outcome
		race(2, func(i int) error {
			if i == 0 {
				_, err := s.Cancel(ctx, tenantA, b.ID, 1, "teste")
				return err
			}
			_, _, bookErr = s.CreateBooking(ctx, tenantA, NewBooking{ResourceID: "sala-1", ClientRef: fmt.Sprintf("b%d", r),
				IdempotencyKey: fmt.Sprintf("kb%d", r), Actor: "teste", Start: from, End: to})
			return bookErr
		}, &out)
		if out.other.Load() != 0 || (bookErr != nil && !errors.Is(bookErr, ErrSlotTaken)) {
			t.Fatalf("round %d: unexpected error %v", r, bookErr)
		}
		if n := activeIn(t, s, tenantA, "sala-1", from, to); n > 1 || (bookErr == nil && n != 1) {
			t.Fatalf("round %d: %d active bookings", r, n)
		}
		if bookErr == nil {
			results["new booking accepted after cancel"]++
		} else {
			results["new booking got 409"]++
		}
	}
	t.Logf("C6 evidence: %v", results)
}

// C7 (SIMULATED failure): a test hook returns an error after the booking row
// is written and before its log, inside the real transaction. Nothing stays, the
// slot is free. It is not a real crash of the database or of the process.
func TestC07SimulatedFailureInsideTransaction(t *testing.T) {
	s, _ := openStore(t)
	seed(t, s, tenantA)
	ctx := context.Background()
	boom := errors.New("falha forçada depois de gravar a reserva")
	s.hook = func(stage string) error {
		if stage == "after_insert" {
			return boom
		}
		return nil
	}
	nb := book("sala-1", "c1", "k1", "2027-01-05 09:00", "2027-01-05 09:40")
	if _, _, err := s.CreateBooking(ctx, tenantA, nb); err == nil {
		t.Fatal("the forced failure must reach the caller")
	}
	if n := countRows(t, s, `SELECT count(*) FROM sched_bookings`) + countRows(t, s, `SELECT count(*) FROM sched_booking_log`); n != 0 {
		t.Fatalf("%d rows left after the failed transaction", n)
	}
	s.hook = nil
	if _, created, err := s.CreateBooking(ctx, tenantA, book("sala-1", "c2", "k2", "2027-01-05 09:00", "2027-01-05 09:40")); err != nil || !created {
		t.Fatalf("slot must be free after the failure: %v", err)
	}
	// Same for a reschedule: a failure after the update keeps the old time.
	b, _, _ := s.CreateBooking(ctx, tenantA, book("sala-1", "c3", "k3", "2027-01-05 11:00", "2027-01-05 11:40"))
	s.hook = func(stage string) error {
		if stage == "after_update" {
			return boom
		}
		return nil
	}
	if _, err := s.Reschedule(ctx, tenantA, b.ID, 1, at("2027-01-05 15:00"), at("2027-01-05 15:40"), "teste"); err == nil {
		t.Fatal("forced failure on reschedule must reach the caller")
	}
	if n := activeIn(t, s, tenantA, "sala-1", at("2027-01-05 11:00"), at("2027-01-05 11:40")); n != 1 {
		t.Fatal("old time must still be held after a failed move")
	}
	if n := activeIn(t, s, tenantA, "sala-1", at("2027-01-05 15:00"), at("2027-01-05 15:40")); n != 0 {
		t.Fatal("new time must not be held after a failed move")
	}
}

// C8 (SIMULATED connection drop): the commit really happens, then a test hook
// makes the call return an error as if the answer was lost on the network. No
// real connection is cut. The client repeats with the same idempotency key: one
// booking, the same one.
func TestC08SimulatedLostAnswerAndRetryWithSameKey(t *testing.T) {
	s, _ := openStore(t)
	seed(t, s, tenantA)
	ctx := context.Background()
	var dropped atomic.Bool
	s.hook = func(stage string) error {
		if stage == "after_commit" && !dropped.Swap(true) {
			return errors.New("conexão caiu durante o commit")
		}
		return nil
	}
	nb := book("sala-1", "c1", "chave-unica", "2027-01-05 09:00", "2027-01-05 09:40")
	if _, _, err := s.CreateBooking(ctx, tenantA, nb); err == nil {
		t.Fatal("the first call must see the dropped answer")
	}
	again, created, err := s.CreateBooking(ctx, tenantA, nb)
	if err != nil || created {
		t.Fatalf("repeat must return the stored booking: created=%v err=%v", created, err)
	}
	third, _, _ := s.CreateBooking(ctx, tenantA, nb)
	if again.ID != third.ID || countRows(t, s, `SELECT count(*) FROM sched_bookings`) != 1 || countRows(t, s, `SELECT count(*) FROM sched_booking_log`) != 1 {
		t.Fatal("expected exactly one booking and one log row")
	}
	// The same key with different content is refused, not silently merged.
	other := book("sala-1", "c1", "chave-unica", "2027-01-05 10:00", "2027-01-05 10:40")
	if _, _, err := s.CreateBooking(ctx, tenantA, other); !errors.Is(err, ErrIdempotencyMismatch) {
		t.Fatalf("reused key with other content: %v", err)
	}
	// Repeats racing each other also end in one booking.
	s.hook = nil
	var out outcome
	race(10, func(int) error {
		_, _, err := s.CreateBooking(ctx, tenantA, book("sala-1", "c9", "chave-corrida", "2027-01-06 09:00", "2027-01-06 09:40"))
		return err
	}, &out)
	if out.ok.Load() != 10 || countRows(t, s, `SELECT count(*) FROM sched_bookings WHERE idempotency_key = 'chave-corrida'`) != 1 {
		t.Fatalf("racing repeats: ok=%d slot_taken=%d other=%d", out.ok.Load(), out.slotTaken.Load(), out.other.Load())
	}
}

// C9: serialization failures and deadlocks are retried up to 3 times with a
// growing wait, then answer ErrUnavailable with nothing written. The retries
// use errors injected by a test hook (SIMULATED); a separate real PostgreSQL
// deadlock checks that the classifier recognizes the real error.
func TestC09SerializationAndDeadlockRetry(t *testing.T) {
	s, dsn := openStore(t)
	seed(t, s, tenantA)
	ctx := context.Background()
	var calls atomic.Int64
	s.hook = func(stage string) error {
		if stage == "after_insert" {
			calls.Add(1)
			return &pgconn.PgError{Code: "40001", Message: "could not serialize access"}
		}
		return nil
	}
	started := time.Now()
	_, _, err := s.CreateBooking(ctx, tenantA, book("sala-1", "c1", "k1", "2027-01-05 09:00", "2027-01-05 09:40"))
	if !errors.Is(err, ErrUnavailable) || calls.Load() != 1+MaxRetries {
		t.Fatalf("want ErrUnavailable after %d attempts, got %v after %d", 1+MaxRetries, err, calls.Load())
	}
	if waited := time.Since(started); waited < 140*time.Millisecond { // 20 + 40 + 80 ms
		t.Fatalf("retries did not back off: %v", waited)
	}
	if n := countRows(t, s, `SELECT count(*) FROM sched_bookings`) + countRows(t, s, `SELECT count(*) FROM sched_booking_log`); n != 0 {
		t.Fatalf("%d partial rows", n)
	}
	calls.Store(0)
	s.hook = func(stage string) error {
		if stage == "after_insert" && calls.Add(1) <= 2 {
			return &pgconn.PgError{Code: "40P01", Message: "deadlock detected"}
		}
		return nil
	}
	if _, created, err := s.CreateBooking(ctx, tenantA, book("sala-1", "c1", "k1", "2027-01-05 09:00", "2027-01-05 09:40")); err != nil || !created || calls.Load() != 3 {
		t.Fatalf("two deadlocks then success: err=%v attempts=%d", err, calls.Load())
	}
	if countRows(t, s, `SELECT count(*) FROM sched_bookings`) != 1 || countRows(t, s, `SELECT count(*) FROM sched_booking_log`) != 1 {
		t.Fatal("expected one booking and one log after the retries")
	}

	// A real deadlock: two transactions lock the same two rows in opposite order.
	s.hook = nil
	widePool(t, s, dsn, 4)
	for _, id := range []string{"r-x", "r-y"} {
		_ = s.PutResource(ctx, tenantA, Resource{ID: id, Kind: "room", Label: id, Capacity: 1, Active: true})
	}
	errs := make([]error, 2)
	var ready, wg sync.WaitGroup
	ready.Add(2)
	wg.Add(2)
	for i, order := range [][2]string{{"r-x", "r-y"}, {"r-y", "r-x"}} {
		go func(i int, order [2]string) {
			defer wg.Done()
			errs[i] = pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
				if _, err := tx.Exec(ctx, `UPDATE sched_resources SET label = label WHERE tenant_id = $1 AND id = $2`, tenantA, order[0]); err != nil {
					return err
				}
				ready.Done()
				ready.Wait()
				_, err := tx.Exec(ctx, `UPDATE sched_resources SET label = label WHERE tenant_id = $1 AND id = $2`, tenantA, order[1])
				return err
			})
		}(i, order)
	}
	wg.Wait()
	deadlocks := 0
	for _, e := range errs {
		if e != nil {
			if !retryable(e) {
				t.Fatalf("real deadlock not recognized as retryable: %v", e)
			}
			deadlocks++
		}
	}
	if deadlocks != 1 {
		t.Fatalf("expected exactly one real deadlock victim, got %d", deadlocks)
	}
	t.Logf("C9 evidence: 40001 x4 -> ErrUnavailable with backoff; 40P01 x2 -> success on attempt 3; real PostgreSQL deadlock classified as retryable (scenario total %v)", time.Since(started).Round(time.Millisecond))
}

// C10: a group of 3 places and 10 simultaneous requests, 10 rounds: exactly 3.
func TestC10GroupCapacity(t *testing.T) {
	s, dsn := openStore(t)
	seed(t, s, tenantA)
	widePool(t, s, dsn, 20)
	ctx := context.Background()
	for r := 0; r < 10; r++ {
		day := time.Date(2027, 4, 5, 0, 0, 0, 0, sp).AddDate(0, 0, 7*(r/5)+r%5)
		from, to := day.Add(14*time.Hour), day.Add(15*time.Hour)
		var out outcome
		race(10, func(i int) error {
			_, _, err := s.CreateBooking(ctx, tenantA, NewBooking{ResourceID: "grupo-1", ClientRef: fmt.Sprintf("g%d-%d", r, i),
				IdempotencyKey: fmt.Sprintf("g%d-%d", r, i), Actor: "teste", Start: from, End: to})
			return err
		}, &out)
		if out.ok.Load() != 3 || out.slotTaken.Load() != 7 {
			t.Fatalf("round %d: ok=%d slot_taken=%d other=%d", r, out.ok.Load(), out.slotTaken.Load(), out.other.Load())
		}
		if n := activeIn(t, s, tenantA, "grupo-1", from, to); n != 3 {
			t.Fatalf("round %d: %d places taken", r, n)
		}
	}
	// A partial overlap also takes a place.
	from := time.Date(2027, 4, 5, 14, 30, 0, 0, sp)
	if _, _, err := s.CreateBooking(ctx, tenantA, NewBooking{ResourceID: "grupo-1", ClientRef: "tarde", IdempotencyKey: "tarde",
		Actor: "teste", Start: from, End: from.Add(time.Hour)}); !errors.Is(err, ErrSlotTaken) {
		t.Fatalf("partial overlap with a full group: %v", err)
	}
}

// C11: the same slot in two tenants, at the same time: both accepted.
func TestC11SameSlotDifferentTenants(t *testing.T) {
	s, dsn := openStore(t)
	seed(t, s, tenantA)
	seed(t, s, tenantB)
	widePool(t, s, dsn, 10)
	ctx := context.Background()
	tenants := []string{tenantA, tenantB}
	var out outcome
	race(2, func(i int) error {
		_, _, err := s.CreateBooking(ctx, tenants[i], book("sala-1", "mesmo-cliente", "mesma-chave", "2027-01-05 09:00", "2027-01-05 09:40"))
		return err
	}, &out)
	if out.ok.Load() != 2 {
		t.Fatalf("both tenants must be accepted: ok=%d slot_taken=%d other=%d", out.ok.Load(), out.slotTaken.Load(), out.other.Load())
	}
	for _, tn := range tenants {
		if n := activeIn(t, s, tn, "sala-1", at("2027-01-05 09:00"), at("2027-01-05 09:40")); n != 1 {
			t.Fatalf("%s: %d bookings", tn, n)
		}
	}
}

// Latency of the queue: how long the last of 50 requests waits when all of them
// want the same resource, against 50 requests on 50 different resources. It
// only measures (numbers depend on the machine); it fails only if the last
// request takes longer than the store's own timeout would allow.
func TestLatencyOfFiftyRequestsOnOneResource(t *testing.T) {
	s, dsn := openStore(t)
	seed(t, s, tenantA)
	for i := 0; i < 50; i++ {
		if err := s.PutResource(context.Background(), tenantA, Resource{ID: fmt.Sprintf("sala-x%d", i), Kind: "room", Label: "Sala", Capacity: 1, Active: true}); err != nil {
			t.Fatal(err)
		}
	}
	widePool(t, s, dsn, 52)
	ctx := context.Background()
	measure := func(round int, sameResource bool) []time.Duration {
		day := time.Date(2027, 6, 1, 0, 0, 0, 0, sp).AddDate(0, 0, round)
		from := day.Add(9 * time.Hour)
		took := make([]time.Duration, 50)
		var out outcome
		race(50, func(i int) error {
			resource := "sala-1"
			if !sameResource {
				resource = fmt.Sprintf("sala-x%d", i)
			}
			started := time.Now()
			_, _, err := s.CreateBooking(ctx, tenantA, NewBooking{ResourceID: resource, ClientRef: fmt.Sprintf("lat-%d-%t-%d", round, sameResource, i),
				IdempotencyKey: fmt.Sprintf("lat-%d-%t-%d", round, sameResource, i), Actor: "teste", Start: from, End: from.Add(40 * time.Minute)})
			took[i] = time.Since(started)
			return err
		}, &out)
		if out.other.Load() != 0 {
			t.Fatalf("unexpected errors: %v", out.firstOther.Load())
		}
		sort.Slice(took, func(a, b int) bool { return took[a] < took[b] })
		return took
	}
	pct := func(d []time.Duration, p float64) time.Duration {
		return d[int(p*float64(len(d)-1))].Round(100 * time.Microsecond)
	}
	const rounds = 10
	var same, spread [][]time.Duration
	for r := 0; r < rounds; r++ {
		same = append(same, measure(r, true))
		spread = append(spread, measure(r+rounds, false))
	}
	report := func(name string, all [][]time.Duration) time.Duration {
		flat := []time.Duration{}
		worst := time.Duration(0)
		for _, d := range all {
			flat = append(flat, d...)
			if d[len(d)-1] > worst {
				worst = d[len(d)-1]
			}
		}
		sort.Slice(flat, func(a, b int) bool { return flat[a] < flat[b] })
		t.Logf("latency %s: %d requests, p50 %v, p95 %v, p99 %v, 50th request worst round %v",
			name, len(flat), pct(flat, 0.50), pct(flat, 0.95), pct(flat, 0.99), worst.Round(100*time.Microsecond))
		return worst
	}
	worst := report("same resource (1 wins, 49 refused)", same)
	report("50 different resources (50 accepted)", spread)
	if worst > dbTimeout/2 {
		t.Fatalf("the last request waited %v", worst)
	}
}
