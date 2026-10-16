package application

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/JustIkra/umir-warehouse/contract"
	"github.com/JustIkra/umir-warehouse/internal/domain"
)

// readLog records which journal reads an Owner makes.
type readLog struct {
	EventStore
	mu     sync.Mutex
	scans  []int64
	reads  []int64
	events []eventsRead
	period []PeriodQuery
	// enterPeriod, when set, is closed-over by the test to hold ReadPeriod.
	enterPeriod chan struct{}
	holdPeriod  chan struct{}
}

type eventsRead struct {
	after int64
	limit int
}

func (s *readLog) ScanBatches(ctx context.Context, after int64, fn func(domain.RecordedBatch) error) error {
	s.mu.Lock()
	s.scans = append(s.scans, after)
	s.mu.Unlock()
	return s.EventStore.ScanBatches(ctx, after, fn)
}

func (s *readLog) ReadBatches(ctx context.Context, after int64) ([]domain.RecordedBatch, error) {
	s.mu.Lock()
	s.reads = append(s.reads, after)
	s.mu.Unlock()
	return s.EventStore.ReadBatches(ctx, after)
}

func (s *readLog) ReadEvents(ctx context.Context, after int64, limit int) ([]domain.Event, error) {
	s.mu.Lock()
	s.events = append(s.events, eventsRead{after, limit})
	s.mu.Unlock()
	return s.EventStore.ReadEvents(ctx, after, limit)
}

func (s *readLog) ReadPeriod(ctx context.Context, query PeriodQuery) ([]domain.Event, error) {
	s.mu.Lock()
	s.period = append(s.period, query)
	enter, hold := s.enterPeriod, s.holdPeriod
	s.mu.Unlock()
	if enter != nil {
		close(enter)
		select {
		case <-hold:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return s.EventStore.ReadPeriod(ctx, query)
}

// journalReads is the number of reads that walk the journal from its start or
// in full.
func (s *readLog) journalReads() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	count := 0
	for _, after := range append(append([]int64(nil), s.scans...), s.reads...) {
		if after == 0 {
			count++
		}
	}
	return count
}

func (s *readLog) reset() {
	s.mu.Lock()
	s.scans, s.reads, s.events, s.period = nil, nil, nil, nil
	s.mu.Unlock()
}

func commitDish(commandID, proposalID string) contract.CommitOrderRequest {
	return contract.CommitOrderRequest{CommandID: commandID, ProposalID: proposalID, AcceptedPositions: []contract.AcceptedPosition{{LineNo: 1, DishID: "d", Portions: 1}}}
}

func recoveredOwner(t *testing.T, at time.Time) (*Owner, *readLog, *testClock) {
	t.Helper()
	log := &readLog{EventStore: seedStore(t, at.Add(72*time.Hour), 20000, 0)}
	clock := &testClock{now: at}
	owner := w3Owner(t, log, clock)
	if err := owner.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	log.reset()
	return owner, log, clock
}

func TestCommitDoesNotReadJournal(t *testing.T) {
	ctx := context.Background()
	at := moscowTime(2026, time.April, 1, 10)
	owner, log, _ := recoveredOwner(t, at)
	result, err := owner.CommitAcceptedOrder(ctx, commitDish("c1", "p1"))
	if err != nil || result.Order == nil {
		t.Fatalf("CommitAcceptedOrder() = %+v, %v", result, err)
	}
	// Reads that follow a commit are bounded too.
	if _, found, err := owner.FindRecordedCommand(ctx, "c1"); err != nil || !found {
		t.Fatalf("FindRecordedCommand() found %v, %v", found, err)
	}
	if _, err := owner.Head(ctx); err != nil {
		t.Fatal(err)
	}
	if _, found, err := owner.FindOrder(ctx, result.Order.OrderID); err != nil || !found {
		t.Fatalf("FindOrder() found %v, %v", found, err)
	}
	if _, err := owner.ReadHistoryPage(ctx, 0, 500); err != nil {
		t.Fatal(err)
	}
	if reads := log.journalReads(); reads != 0 {
		t.Fatalf("the commit and the reads after it walked the journal %d times; want 0", reads)
	}
}

func TestHeadAndOrderLookups(t *testing.T) {
	ctx := context.Background()
	at := moscowTime(2026, time.April, 1, 10)
	owner, _, clock := recoveredOwner(t, at)
	head, err := owner.Head(ctx)
	if err != nil || head.StateVersion != 3 || !head.EvaluatedAt.Equal(at) {
		t.Fatalf("Head() = %+v, %v; want version 3 at %v", head, err, at)
	}
	clock.Set(at.Add(time.Minute))
	result, err := owner.CommitAcceptedOrder(ctx, commitDish("c1", "p1"))
	if err != nil || result.Order == nil {
		t.Fatalf("CommitAcceptedOrder() = %+v, %v", result, err)
	}
	placement, found, err := owner.FindOrderPlacement(ctx, result.Order.OrderID)
	if err != nil || !found || placement.Sequence != 4 || !placement.RecordedAt.Equal(at.Add(time.Minute)) {
		t.Fatalf("FindOrderPlacement() = %+v, %v, %v; want sequence 4 at %v", placement, found, err, at.Add(time.Minute))
	}
	order, found, err := owner.FindOrder(ctx, result.Order.OrderID)
	if err != nil || !found || order.OrderID != result.Order.OrderID || order.ProposalID != "p1" {
		t.Fatalf("FindOrder() = %+v, %v, %v", order, found, err)
	}
	if _, found, err := owner.FindOrder(ctx, "missing"); err != nil || found {
		t.Fatalf("FindOrder(missing) found %v, %v", found, err)
	}
	// A second command for the same proposal answers with the placement of the first.
	again, err := owner.CommitAcceptedOrder(ctx, commitDish("c2", "p1"))
	if err != nil || again.Order == nil || again.Order.OrderID != result.Order.OrderID {
		t.Fatalf("CommitAcceptedOrder(c2) = %+v, %v; want the first order", again, err)
	}
	if head, err := owner.Head(ctx); err != nil || head.StateVersion != 4 {
		t.Fatalf("Head() after the repeat = %+v, %v; want version 4", head, err)
	}
}

func TestHistoryPageReadsOnlyWindow(t *testing.T) {
	ctx := context.Background()
	at := moscowTime(2026, time.April, 1, 10)
	owner, log, _ := recoveredOwner(t, at)
	for _, id := range []string{"a", "b", "c"} {
		if _, err := owner.CommitAcceptedOrder(ctx, commitDish("cmd-"+id, "proposal-"+id)); err != nil {
			t.Fatal(err)
		}
	}
	log.reset()
	page, err := owner.ReadHistoryPage(ctx, 3, 2)
	if err != nil {
		t.Fatal(err)
	}
	if page.StateVersion != 6 || len(page.Events) != 2 || page.Events[0].Sequence != 4 || page.Events[1].Sequence != 5 {
		t.Fatalf("page = version %d with %d events from %v; want version 6 and events 4, 5", page.StateVersion, len(page.Events), page.Events)
	}
	if !page.EvaluatedAt.Equal(at) {
		t.Fatalf("EvaluatedAt = %v; want %v", page.EvaluatedAt, at)
	}
	log.mu.Lock()
	defer log.mu.Unlock()
	if len(log.events) != 1 || log.events[0] != (eventsRead{3, 2}) || len(log.scans)+len(log.reads) != 0 {
		t.Fatalf("store reads: events %v, scans %v, batch reads %v; want one window of 2 after 3", log.events, log.scans, log.reads)
	}
}

func TestMovementsReadsOnlyPeriod(t *testing.T) {
	ctx := context.Background()
	at := moscowTime(2026, time.April, 1, 10)
	owner, log, clock := recoveredOwner(t, at)
	if _, err := owner.CommitAcceptedOrder(ctx, commitDish("c1", "p1")); err != nil {
		t.Fatal(err)
	}
	analytics := w5Analytics(t, owner, clock, at.Add(-48*time.Hour))
	log.reset()
	report, err := analytics.Movements(ctx, w5Request())
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Items) != 1 || report.Items[0].ProductID != "p" || report.Items[0].Name != "Product" || report.Items[0].ReceivedQuantity != 20000 || report.Items[0].OrderedQuantity != 6000 || report.Identity.StateVersion != 4 {
		t.Fatalf("report = %+v", report)
	}
	log.mu.Lock()
	defer log.mu.Unlock()
	if len(log.period) != 1 || len(log.scans)+len(log.reads)+len(log.events) != 0 {
		t.Fatalf("store reads: period %v, scans %v, batch reads %v, event reads %v; want a single period query", log.period, log.scans, log.reads, log.events)
	}
	if query := log.period[0]; query.UpToSequence != 4 || !query.From.Before(query.To) {
		t.Fatalf("period query = %+v; want it bounded by the captured version 4", query)
	}
}

// Planning holds the owner only to capture the version and a shallow view;
// the period facts are read and summed without it.
func TestPlanningCaptureDoesNotBlockCommits(t *testing.T) {
	ctx := context.Background()
	at := moscowTime(2026, time.April, 1, 10)
	owner, log, clock := recoveredOwner(t, at)
	if _, err := owner.CommitAcceptedOrder(ctx, commitDish("early", "early-proposal")); err != nil {
		t.Fatal(err)
	}
	// The period ends before the clock, so the early order is inside it.
	clock.Set(at.Add(time.Minute))
	planning, err := NewPlanning(owner, at.Add(-168*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	log.mu.Lock()
	log.enterPeriod, log.holdPeriod = make(chan struct{}), make(chan struct{})
	enter, hold := log.enterPeriod, log.holdPeriod
	log.mu.Unlock()

	type outcome struct {
		result contract.RecommendationInputs
		err    error
	}
	done := make(chan outcome, 1)
	go func() {
		result, err := planning.Recommendations(ctx, contract.PlanningRequest{Scope: w3Scope(), Actor: w3Actor})
		done <- outcome{result, err}
	}()
	select {
	case <-enter:
	case <-time.After(10 * time.Second):
		t.Fatal("planning never read the period")
	}
	committed := make(chan error, 1)
	go func() {
		_, err := owner.CommitAcceptedOrder(ctx, commitDish("late", "late-proposal"))
		committed <- err
	}()
	select {
	case err := <-committed:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("a commit waited for planning to finish its period read")
	}
	close(hold)
	got := <-done
	if got.err != nil {
		t.Fatal(got.err)
	}
	if got.result.Identity.StateVersion != 4 {
		t.Fatalf("planning version = %d; want the captured version 4", got.result.Identity.StateVersion)
	}
	if len(got.result.Demand) != 1 || got.result.Demand[0].DishID != "d" || got.result.Demand[0].Portions != 1 {
		t.Fatalf("demand = %+v; want only the early order's one portion", got.result.Demand)
	}
}
