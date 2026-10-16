package application

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/JustIkra/umir-warehouse/contract"
	"github.com/JustIkra/umir-warehouse/internal/adapters/memory"
	"github.com/JustIkra/umir-warehouse/internal/domain"
)

func w5Analytics(t *testing.T, owner *Owner, clock *testClock, coverage time.Time) *Analytics {
	t.Helper()
	n := 0
	a, err := NewAnalytics(ownerStock{owner}, owner, coverage, clock.Now, func() string { n++; return fmt.Sprintf("cursor-%d", n) }, domain.LoadZone)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

type ownerStock struct{ o *Owner }

func (s ownerStock) PrepareStock(ctx context.Context) (contract.StockPreparation, error) {
	return s.o.PrepareStock(ctx)
}
func (s ownerStock) CalculateStock(ctx context.Context, p contract.StockPreparation) (contract.InventoryView, error) {
	return CalculateStock(ctx, p)
}
func w5Request() contract.AnalyticsRequest {
	return contract.AnalyticsRequest{Scope: w3Scope(), Actor: w3Actor, ExpectedSettingsVersion: "1"}
}

func TestW5MovementsActualFactsHalfOpenAndZeroStock(t *testing.T) {
	at := moscowTime(2026, time.April, 1, 10)
	clock := &testClock{now: at}
	store := seedStore(t, at.Add(time.Hour), 10000, 6000)
	owner := w3Owner(t, store, clock)
	a := w5Analytics(t, owner, clock, at.Add(-time.Hour))
	ctx := context.Background()
	clock.Set(at.Add(time.Hour))
	if _, err := owner.PrepareStock(ctx); err != nil {
		t.Fatal(err)
	}
	before, err := a.Movements(ctx, w5Request())
	if err != nil {
		t.Fatal(err)
	}
	if len(before.Items) != 1 || before.Items[0].ReceivedQuantity != 10000 || before.Items[0].OrderedQuantity != 6000 || before.Items[0].WrittenOffQuantity != 0 {
		t.Fatalf("pending not movement %+v", before)
	}
	result, err := owner.ConfirmWriteoff(ctx, contract.ConfirmWriteoffRequest{Scope: w3Scope(), ActorUserID: w3Actor, CommandID: "final", BatchID: "batch"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = owner.ConfirmWriteoff(ctx, contract.ConfirmWriteoffRequest{Scope: w3Scope(), ActorUserID: w3Actor, CommandID: "final", BatchID: "batch"}); err != nil {
		t.Fatal(err)
	}
	after, err := a.Movements(ctx, w5Request())
	if err != nil {
		t.Fatal(err)
	}
	if len(after.Items) != 1 || after.Items[0].WrittenOffQuantity != 4000 || after.Identity.StateVersion != result.StateVersion {
		t.Fatalf("final/replay %+v", after)
	}
	request := w5Request()
	request.FromDate = "2026-04-02"
	request.ThroughDate = "2026-04-02"
	excluded, err := a.Movements(ctx, request)
	if err != nil || len(excluded.Items) != 0 {
		t.Fatalf("RecordedAt outside period %+v %v", excluded, err)
	}
	boundary := moscowTime(2026, time.April, 2, 0)
	clock.Set(boundary)
	if _, err = owner.ReceiveDelivery(ctx, contract.ReceiveDeliveryRequest{CommandID: "boundary", DeliveryID: "boundary", Number: "1", CreatedAt: at, Lines: []contract.DeliveryLine{{ProductID: "p", Quantity: 7, ManufacturedAt: at, ReceivedAt: at, ShelfLife: contract.ShelfLife{Value: 3, Unit: contract.ShelfLifeDay}}}}); err != nil {
		t.Fatal(err)
	}
	included, err := a.Movements(ctx, request)
	if err != nil || len(included.Items) != 1 || included.Items[0].ReceivedQuantity != 7 {
		t.Fatalf("from excluded %+v %v", included, err)
	}
	request.FromDate = "2026-04-01"
	request.ThroughDate = "2026-04-01"
	excluded, err = a.Movements(ctx, request)
	if err != nil || excluded.Items[0].ReceivedQuantity != 10000 {
		t.Fatalf("to included %+v %v", excluded, err)
	}
}
func TestW5HistoricalExpiryIsFinalMovement(t *testing.T) {
	at := moscowTime(2026, time.April, 2, 10)
	clock := &testClock{now: at}
	store := seedStore(t, at, 10000, 6000)
	owner, err := NewOwner(store, clock.Now, func() string { return "expiry-event" })
	if err != nil {
		t.Fatal(err)
	}
	if _, err = owner.ProcessDueExpirations(context.Background()); err != nil {
		t.Fatal(err)
	}
	a := w5Analytics(t, owner, clock, at.Add(-24*time.Hour))
	report, err := a.Movements(context.Background(), w5Request())
	if err != nil || report.Items[0].WrittenOffQuantity != 4000 {
		t.Fatalf("historical final=%+v %v", report, err)
	}
}
func TestW5CapturedPaginationReplayAndBindings(t *testing.T) {
	at := moscowTime(2026, time.April, 2, 10)
	clock := &testClock{now: at}
	owner := w3Owner(t, memory.NewEventStore(), clock)
	ctx := context.Background()
	for _, id := range []string{"a", "b", "c"} {
		if _, err := owner.RegisterProduct(ctx, contract.RegisterProductRequest{CommandID: id, ProductID: id, Name: "Cafe\u0301", BaseUnit: contract.BaseUnitG}); err != nil {
			t.Fatal(err)
		}
	}
	a := w5Analytics(t, owner, clock, at)
	request := w5Request()
	request.Limit = 1
	first, err := a.Stock(ctx, request)
	if err != nil || len(first.Items) != 1 || first.NextCursor == "" {
		t.Fatalf("first %+v %v", first, err)
	}
	request.Cursor = first.NextCursor
	if _, err = owner.RegisterProduct(ctx, contract.RegisterProductRequest{CommandID: "new", ProductID: "aa", Name: "Other", BaseUnit: contract.BaseUnitPCS}); err != nil {
		t.Fatal(err)
	}
	second, err := a.Stock(ctx, request)
	if err != nil || second.Items[0].ProductID != "b" || second.Identity != first.Identity {
		t.Fatalf("prefix mixed %+v %v", second, err)
	}
	second.Items[0].Name = "mutation"
	replay, err := a.Stock(ctx, request)
	if err != nil || replay.Items[0].Name == "mutation" {
		t.Fatalf("alias/replay %+v %v", replay, err)
	}
	for _, mutate := range []func(*contract.AnalyticsRequest){func(r *contract.AnalyticsRequest) { r.Actor = "other" }, func(r *contract.AnalyticsRequest) { r.Scope.RestaurantID = "77777777-7777-4777-8777-777777777777" }, func(r *contract.AnalyticsRequest) { r.Scope.RuntimeEpoch = "88888888-8888-4888-8888-888888888888" }, func(r *contract.AnalyticsRequest) { r.Search = "other" }, func(r *contract.AnalyticsRequest) { r.RiskHours = 49 }, func(r *contract.AnalyticsRequest) { r.Limit = 2 }, func(r *contract.AnalyticsRequest) { r.Scope.SettingsVersion = "2" }} {
		changed := request
		mutate(&changed)
		if _, err := a.Stock(ctx, changed); !errors.Is(err, contract.ErrRefreshRequired) {
			t.Fatalf("cursor not bound %+v %v", changed, err)
		}
	}
	request.Cursor = replay.NextCursor
	last, err := a.Stock(ctx, request)
	if err != nil || len(last.Items) != 1 || last.Items[0].ProductID != "c" || last.NextCursor != "" {
		t.Fatalf("end %+v %v", last, err)
	}
	request.Cursor = first.NextCursor
	clock.Set(at.Add(5 * time.Minute))
	if _, err := a.Stock(ctx, request); !errors.Is(err, contract.ErrRefreshRequired) {
		t.Fatalf("expired=%v", err)
	}
	request = w5Request()
	request.Search = "CAFÉ"
	report, err := a.Stock(ctx, request)
	if err != nil || len(report.Items) != 3 {
		t.Fatalf("NFC/casefold=%+v %v", report, err)
	}
	request.ProductID = "b"
	report, err = a.Stock(ctx, request)
	if err != nil || len(report.Items) != 1 || report.Items[0].ProductID != "b" {
		t.Fatalf("intersection=%+v %v", report, err)
	}
}
func TestW5JournalSurvivesStockFailureAndRejectsUnknownSchema(t *testing.T) {
	at := moscowTime(2026, time.April, 2, 10)
	clock := &testClock{now: at}
	base := seedStore(t, at, 10000, 0)
	fault := &reviewRejectStore{EventStore: base, armed: true, rejectAt: 1}
	owner := w3Owner(t, fault, clock)
	a := w5Analytics(t, owner, clock, at.Add(-48*time.Hour))
	ctx := context.Background()
	if _, err := a.Stock(ctx, w5Request()); !errors.Is(err, contract.ErrInventoryUnavailable) {
		t.Fatalf("stock fault=%v", err)
	}
	report, err := a.Movements(ctx, w5Request())
	if err != nil || report.Items[0].ReceivedQuantity != 10000 || report.Items[0].WrittenOffQuantity != 0 {
		t.Fatalf("independent journal=%+v %v", report, err)
	}
	badSchema := &periodSchemaStore{EventStore: base}
	bad := w3Owner(t, badSchema, clock)
	badAnalytics := w5Analytics(t, bad, clock, at)
	if _, err := badAnalytics.Movements(ctx, w5Request()); !errors.Is(err, contract.ErrUnknownEventSchema) {
		t.Fatalf("unknown schema=%v", err)
	}
}

// periodSchemaStore returns the events of a period with an unknown schema.
type periodSchemaStore struct{ EventStore }

func (s *periodSchemaStore) ReadPeriod(ctx context.Context, query PeriodQuery) ([]domain.Event, error) {
	events, err := s.EventStore.ReadPeriod(ctx, query)
	for i := range events {
		events[i].SchemaVersion = 99
	}
	return events, err
}
func TestW5ConcurrentReportsCaptureOnePrefix(t *testing.T) {
	at := moscowTime(2026, time.April, 2, 10)
	clock := &testClock{now: at}
	owner := w3Owner(t, memory.NewEventStore(), clock)
	ctx := context.Background()
	a := w5Analytics(t, owner, clock, at)
	var wg sync.WaitGroup
	for i := 0; i < 30; i++ {
		wg.Add(2)
		go func(i int) {
			defer wg.Done()
			id := fmt.Sprintf("p-%02d", i)
			if _, err := owner.RegisterProduct(ctx, contract.RegisterProductRequest{CommandID: id, ProductID: id, Name: "P", BaseUnit: contract.BaseUnitG}); err != nil {
				t.Error(err)
			}
		}(i)
		go func() {
			defer wg.Done()
			report, err := a.Stock(ctx, w5Request())
			if err != nil {
				t.Error(err)
				return
			}
			if int64(len(report.Items)) != report.Identity.StateVersion {
				t.Errorf("mixed prefix rows=%d version=%d", len(report.Items), report.Identity.StateVersion)
			}
		}()
	}
	wg.Wait()
}
func TestW5MovementPaginationKeepsPeriodAndCoverage(t *testing.T) {
	at := moscowTime(2026, time.April, 2, 10)
	clock := &testClock{now: at}
	owner := w3Owner(t, memory.NewEventStore(), clock)
	ctx := context.Background()
	a := w5Analytics(t, owner, clock, at)
	for _, id := range []string{"a", "b"} {
		if _, err := owner.RegisterProduct(ctx, contract.RegisterProductRequest{CommandID: id, ProductID: id, Name: "P", BaseUnit: contract.BaseUnitG}); err != nil {
			t.Fatal(err)
		}
		if _, err := owner.ReceiveDelivery(ctx, contract.ReceiveDeliveryRequest{CommandID: "d" + id, DeliveryID: id, Number: "1", CreatedAt: at, Lines: []contract.DeliveryLine{{ProductID: id, Quantity: 2, ManufacturedAt: at, ReceivedAt: at, ShelfLife: contract.ShelfLife{Value: 2, Unit: contract.ShelfLifeDay}}}}); err != nil {
			t.Fatal(err)
		}
	}
	r := w5Request()
	r.Limit = 1
	first, err := a.Movements(ctx, r)
	if err != nil {
		t.Fatal(err)
	}
	r.Cursor = first.NextCursor
	clock.Set(at.Add(24 * time.Hour))
	second, err := a.Movements(ctx, r)
	if !errors.Is(err, contract.ErrRefreshRequired) {
		t.Fatalf("expired cursor %v %+v", err, second)
	}
	clock.Set(at.Add(time.Minute))
	_, err = a.Movements(ctx, r)
	if !errors.Is(err, contract.ErrRefreshRequired) {
		t.Fatalf("evicted cursor revived=%v", err)
	}
	clock.Set(at)
	r.Cursor = ""
	first, err = a.Movements(ctx, r)
	if err != nil {
		t.Fatal(err)
	}
	r.Cursor = first.NextCursor
	second, err = a.Movements(ctx, r)
	if err != nil || !reflect.DeepEqual(first.Coverage, second.Coverage) || !first.From.Equal(second.From) || !first.To.Equal(second.To) || first.Identity != second.Identity || second.Items[0].ProductID != "b" {
		t.Fatalf("movement page %+v %v", second, err)
	}
	r.FromDate = "2026-04-01"
	r.ThroughDate = "2026-04-02"
	if _, err = a.Movements(ctx, r); !errors.Is(err, contract.ErrRefreshRequired) {
		t.Fatalf("period binding=%v", err)
	}
}

func TestW5ReportCapacityCancellationAndCalendarLimits(t *testing.T) {
	at := moscowTime(2026, time.April, 2, 10)
	clock := &testClock{now: at}
	o := w3Owner(t, memory.NewEventStore(), clock)
	ctx := context.Background()
	a := w5Analytics(t, o, clock, at)
	for _, id := range []string{"a", "b"} {
		if _, err := o.RegisterProduct(ctx, contract.RegisterProductRequest{CommandID: id, ProductID: id, Name: "P", BaseUnit: contract.BaseUnitG}); err != nil {
			t.Fatal(err)
		}
	}
	r := w5Request()
	r.Limit = 1
	var first contract.StockReport
	for i := 0; i < 128; i++ {
		report, err := a.Stock(ctx, r)
		if err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			first = report
		}
	}
	if _, err := a.Stock(ctx, r); !errors.Is(err, contract.ErrLimitExceeded) {
		t.Fatalf("capacity=%v", err)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	r.Cursor = first.NextCursor
	if _, err := a.Stock(canceled, r); !errors.Is(err, context.Canceled) {
		t.Errorf("cancelled cached page=%v", err)
	}
	clock.Set(at.Add(5 * time.Minute))
	r.Cursor = ""
	if _, err := a.Stock(ctx, r); err != nil {
		t.Fatalf("capacity not released=%v", err)
	}
	r = w5Request()
	r.Scope.TimeZone = "Missing/Zone"
	if _, err := a.Movements(ctx, r); !errors.Is(err, contract.ErrUnknownTimeZone) {
		t.Fatalf("unknown zone=%v", err)
	}
	r = w5Request()
	r.Scope.TimeZone = "Pacific/Apia"
	r.FromDate = "2011-12-29"
	r.ThroughDate = "2011-12-29"
	if _, err := a.Movements(ctx, r); !errors.Is(err, contract.ErrSkippedDate) {
		t.Fatalf("skipped successor=%v", err)
	}
	r = w5Request()
	r.Scope.TimeZone = "Europe/Berlin"
	r.FromDate = "2026-03-29"
	r.ThroughDate = r.FromDate
	report, err := a.Movements(ctx, r)
	if err != nil || report.To.Sub(report.From) != 23*time.Hour {
		t.Fatalf("short calendar day %+v %v", report, err)
	}
	r.FromDate = "2026-10-25"
	r.ThroughDate = r.FromDate
	report, err = a.Movements(ctx, r)
	if err != nil || report.To.Sub(report.From) != 25*time.Hour {
		t.Fatalf("long calendar day %+v %v", report, err)
	}
}

func TestW5OpaqueProductFilterPreservesFrozenIDDomain(t *testing.T) {
	at := moscowTime(2026, time.April, 2, 10)
	clock := &testClock{now: at}
	o := w3Owner(t, memory.NewEventStore(), clock)
	id := strings.Repeat("🍲", 70)
	if _, err := o.RegisterProduct(context.Background(), contract.RegisterProductRequest{CommandID: "unicode", ProductID: id, Name: "P", BaseUnit: contract.BaseUnitG}); err != nil {
		t.Fatal(err)
	}
	a := w5Analytics(t, o, clock, at)
	request := w5Request()
	request.ProductID = id
	report, err := a.Stock(context.Background(), request)
	if err != nil || len(report.Items) != 1 || report.Items[0].ProductID != id {
		t.Fatalf("opaque ID filter changed frozen domain: rows=%d err=%v", len(report.Items), err)
	}
	request = w5Request()
	request.Cursor = strings.Repeat("opaque", 30)
	if _, err := a.Stock(context.Background(), request); !errors.Is(err, contract.ErrRefreshRequired) {
		t.Fatalf("unknown cursor error=%v", err)
	}
}
