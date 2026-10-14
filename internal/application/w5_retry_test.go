package application

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/JustIkra/umir-warehouse/contract"
	"github.com/JustIkra/umir-warehouse/internal/adapters/memory"
	"github.com/JustIkra/umir-warehouse/internal/domain"
)

func w5CivilDelivery() contract.ReceiveDeliveryRequest {
	s := w3Scope()
	s.TimeZone = "Europe/Berlin"
	return contract.ReceiveDeliveryRequest{Scope: s, ExpectedSettingsVersion: "1", CommandID: "w5-zone", DeliveryID: "w5-zone", Number: "1", Lines: []contract.DeliveryLine{{ProductID: "p", Quantity: 10, ShelfLife: contract.ShelfLife{Value: 2, Unit: contract.ShelfLifeMonth}}}, TimeInputs: &contract.DeliveryTimeInputs{CreatedAt: contract.TimeInput{Instant: "2026-10-01T10:00:00+03:00"}, Lines: []contract.DeliveryLineTimes{{ManufacturedAt: contract.TimeInput{Civil: &contract.CivilInput{LocalDateTime: "2026-08-25T02:30:00"}}, ReceivedAt: contract.TimeInput{Instant: "2026-10-01T10:00:00+03:00"}}}}}
}
func w5ZoneOwner(t *testing.T, store EventStore, clock *testClock) *Owner {
	t.Helper()
	o := w3Owner(t, store, clock)
	if err := o.ConfigureTZData("2025c"); err != nil {
		t.Fatal(err)
	}
	if _, err := o.RegisterProduct(context.Background(), contract.RegisterProductRequest{CommandID: "p", ProductID: "p", Name: "P", BaseUnit: contract.BaseUnitG}); err != nil {
		t.Fatal(err)
	}
	return o
}
func TestW5TimezoneChangeResolvesOriginalRetainedCommandBeforeStaleForm(t *testing.T) {
	at := moscowTime(2026, time.October, 1, 10)
	clock := &testClock{now: at}
	o := w5ZoneOwner(t, memory.NewEventStore(), clock)
	ctx := context.Background()
	request := w5CivilDelivery()
	original, err := o.ReceiveDelivery(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	if original.Scope.TimeZone != "Europe/Berlin" || original.Scope.SettingsVersion != "1" || original.Batches[0].ExpiresAt.UTC().Format(time.RFC3339) != "2026-10-25T00:30:00Z" {
		t.Fatalf("captured month/fold %+v", original)
	}
	request.Scope.TimeZone = "Asia/Tokyo"
	request.Scope.SettingsVersion = "2"
	retry, err := o.ReceiveDelivery(ctx, request)
	if err != nil || !reflect.DeepEqual(original, retry) {
		t.Fatalf("retained retry recalculated %+v %v", retry, err)
	}
	request.CommandID = "fresh"
	request.DeliveryID = "fresh"
	if _, err = o.ReceiveDelivery(ctx, request); !errors.Is(err, contract.ErrSettingsChanged) {
		t.Fatalf("fresh stale=%v", err)
	}
	request.ExpectedSettingsVersion = "2"
	fresh, err := o.ReceiveDelivery(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	if fresh.Scope.TimeZone != "Asia/Tokyo" || fresh.Batches[0].ExpiresAt.Equal(original.Batches[0].ExpiresAt) {
		t.Fatalf("fresh zone not applied %+v", fresh)
	}
	request = w5CivilDelivery()
	request.Scope.OrganizationID = "99999999-9999-4999-8999-999999999999"
	if _, err = o.ReceiveDelivery(ctx, request); !errors.Is(err, contract.ErrForbidden) {
		t.Fatalf("foreign replay=%v", err)
	}
	request = w5CivilDelivery()
	request.Lines[0].Quantity++
	if _, err = o.ReceiveDelivery(ctx, request); !errors.Is(err, contract.ErrCommandIdentityConflict) {
		t.Fatalf("payload conflict=%v", err)
	}
}

type w5UnknownStore struct {
	EventStore
	blocked  bool
	commit   bool
	attempts int
	captured domain.CommandBatch
}

func (s *w5UnknownStore) AppendBatch(ctx context.Context, b domain.CommandBatch) (domain.AppendResult, error) {
	if b.CommandType == "ReceiveDelivery" {
		s.attempts++
		if s.blocked {
			s.captured = b
			if s.commit {
				if _, err := s.EventStore.AppendBatch(ctx, b); err != nil {
					return domain.AppendResult{}, err
				}
			}
			return domain.AppendResult{}, domain.ErrUnknownWriteOutcome
		}
	}
	return s.EventStore.AppendBatch(ctx, b)
}
func (s *w5UnknownStore) FindCommand(ctx context.Context, id string) (domain.CommandRecord, bool, error) {
	if s.blocked && len(s.captured.Events) > 0 && id == "w5-zone" {
		return domain.CommandRecord{}, false, errors.New("synthetic lookup lost")
	}
	return s.EventStore.FindCommand(ctx, id)
}
func TestW5TimezoneRetryRetainedUnknownAndLostCommittedReply(t *testing.T) {
	for _, committed := range []bool{false, true} {
		t.Run(map[bool]string{false: "retained-uncommitted", true: "committed-lost-reply"}[committed], func(t *testing.T) {
			at := moscowTime(2026, time.October, 1, 10)
			clock := &testClock{now: at}
			store := &w5UnknownStore{EventStore: memory.NewEventStore(), commit: committed}
			o := w5ZoneOwner(t, store, clock)
			ctx := context.Background()
			store.blocked = true
			request := w5CivilDelivery()
			if _, err := o.ReceiveDelivery(ctx, request); err == nil {
				t.Fatal("unknown fault not observed")
			}
			if len(store.captured.Events) == 0 {
				t.Fatal("fault did not reach real admission")
			}
			captured := domain.CloneEvent(store.captured.Events[0])
			originalExpiry := captured.Payload.Batches[0].ExpiresAt
			request.Scope.TimeZone = "Asia/Tokyo"
			request.Scope.SettingsVersion = "2"
			clock.Set(at.Add(time.Hour))
			store.blocked = false
			retry, err := o.ReceiveDelivery(ctx, request)
			if err != nil {
				t.Fatal(err)
			}
			if retry.Scope.TimeZone != "Europe/Berlin" || retry.Scope.SettingsVersion != "1" || !retry.Batches[0].ExpiresAt.Equal(originalExpiry) || !retry.EvaluatedAt.Equal(at) {
				t.Fatalf("unknown retry recaptured %+v", retry)
			}
			version, err := store.Version(ctx)
			if err != nil {
				t.Fatal(err)
			}
			again, err := o.ReceiveDelivery(ctx, request)
			if err != nil || !reflect.DeepEqual(retry, again) {
				t.Fatalf("replay %+v %v", again, err)
			}
			after, err := store.Version(ctx)
			if err != nil || after != version {
				t.Fatalf("duplicate fact %d/%d %v", version, after, err)
			}
		})
	}
}
func TestW5CapturedInFlightZoneAndOrganizationIsolation(t *testing.T) {
	at := moscowTime(2026, time.October, 1, 10)
	clock := &testClock{now: at}
	base := memory.NewEventStore()
	reached := make(chan struct{})
	release := make(chan struct{})
	wrapped := &eventStoreWrapper{EventStore: base, append: func(b domain.CommandBatch, r domain.AppendResult, e error) (domain.AppendResult, error) {
		if b.CommandType == "ReceiveDelivery" {
			close(reached)
			<-release
		}
		return r, e
	}}
	o := w5ZoneOwner(t, wrapped, clock)
	ctx := context.Background()
	request := w5CivilDelivery()
	done := make(chan contract.DeliveryReceipt, 1)
	errs := make(chan error, 1)
	go func() { receipt, err := o.ReceiveDelivery(ctx, request); done <- receipt; errs <- err }()
	<-reached
	request.Scope.SettingsVersion = "2"
	request.Scope.TimeZone = "Asia/Tokyo"
	close(release)
	receipt := <-done
	if err := <-errs; err != nil {
		t.Fatal(err)
	}
	if receipt.Scope.TimeZone != "Europe/Berlin" || receipt.Scope.SettingsVersion != "1" {
		t.Fatalf("inflight recaptured %+v", receipt)
	}
	otherID := 0
	other, err := NewOwner(memory.NewEventStore(), clock.Now, func() string { otherID++; return string(rune(100 + otherID)) })
	if err != nil {
		t.Fatal(err)
	}
	s := w3Scope()
	s.OrganizationID = "99999999-9999-4999-8999-999999999999"
	s.RestaurantID = "88888888-8888-4888-8888-888888888888"
	s.TimeZone = "Asia/Tokyo"
	s.SettingsVersion = "5"
	if err = other.ConfigureScope(s); err != nil {
		t.Fatal(err)
	}
	if err = other.ConfigureTZData("2025c"); err != nil {
		t.Fatal(err)
	}
	if _, err = other.RegisterProduct(ctx, contract.RegisterProductRequest{CommandID: "p", ProductID: "p", Name: "P", BaseUnit: contract.BaseUnitG}); err != nil {
		t.Fatal(err)
	}
	request = w5CivilDelivery()
	request.Scope = s
	request.ExpectedSettingsVersion = "5"
	otherReceipt, err := other.ReceiveDelivery(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	if receipt.Batches[0].ExpiresAt.Equal(otherReceipt.Batches[0].ExpiresAt) || otherReceipt.Scope.OrganizationID == receipt.Scope.OrganizationID {
		t.Fatal("organization zones shared")
	}
}
