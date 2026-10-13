package bootstrap_test

import (
	"context"
	"crypto/rand"
	"errors"
	"testing"
	"time"

	"github.com/JustIkra/umir-warehouse/bootstrap"
	"github.com/JustIkra/umir-warehouse/contract"
)

func TestW5RestartResetsCoverageStateAndCursors(t *testing.T) {
	ctx := context.Background()
	at := time.Date(2026, time.October, 13, 9, 0, 0, 0, time.FixedZone("MSK", 10800))
	scope := contract.Scope{OrganizationID: "22222222-2222-4222-8222-222222222222", RestaurantID: "33333333-3333-4333-8333-333333333333", RuntimeEpoch: "44444444-4444-4444-8444-444444444444", LifecycleVersion: "1", TimeZone: "Europe/Berlin", SettingsVersion: "1"}
	options := bootstrap.Options{Now: func() time.Time { return at }, NewID: rand.Text, EventsPerSnapshot: 1}
	first, err := bootstrap.NewRegistry(options, scope.RuntimeEpoch)
	if err != nil {
		t.Fatal(err)
	}
	if err = first.Register(ctx, scope); err != nil {
		t.Fatal(err)
	}
	runtime, err := first.Resolve(ctx, scope)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"a", "b"} {
		if _, err = runtime.RegisterProduct(ctx, contract.RegisterProductRequest{CommandID: id, ProductID: id, Name: "P", BaseUnit: contract.BaseUnitG}); err != nil {
			t.Fatal(err)
		}
	}
	request := contract.AnalyticsRequest{Scope: scope, Actor: "11111111-1111-4111-8111-111111111111", ExpectedSettingsVersion: "1", Limit: 1}
	page, err := runtime.AnalyticsStock(ctx, request)
	if err != nil || page.NextCursor == "" {
		t.Fatalf("first cursor=%+v %v", page, err)
	}
	at = at.Add(time.Hour)
	newScope := scope
	newScope.RuntimeEpoch = "55555555-5555-4555-8555-555555555555"
	restarted, err := bootstrap.NewRegistry(options, newScope.RuntimeEpoch)
	if err != nil {
		t.Fatal(err)
	}
	if err = restarted.Register(ctx, newScope); err != nil {
		t.Fatal(err)
	}
	reset, err := restarted.Resolve(ctx, newScope)
	if err != nil {
		t.Fatal(err)
	}
	request.Scope = newScope
	request.Cursor = page.NextCursor
	if _, err = reset.AnalyticsStock(ctx, request); !errors.Is(err, contract.ErrRefreshRequired) {
		t.Fatalf("old cursor accepted=%v", err)
	}
	if _, err = restarted.Resolve(ctx, scope); !errors.Is(err, contract.ErrRuntimeReset) {
		t.Fatalf("old epoch=%v", err)
	}
	request.Cursor = ""
	movements, err := reset.AnalyticsMovements(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	if movements.Coverage.Complete || !movements.Coverage.CoverageFrom.Equal(at) || movements.Coverage.Epoch != newScope.RuntimeEpoch || movements.Identity.StateVersion != 0 || len(movements.Items) != 0 {
		t.Fatalf("reset history invented=%+v", movements)
	}
	stocks, err := reset.AnalyticsStock(ctx, request)
	if err != nil || len(stocks.Items) != 0 || stocks.Identity.StateVersion != 0 {
		t.Fatalf("business reset=%+v %v", stocks, err)
	}
}
func TestW5MismatchedPackagedDataFailsComposition(t *testing.T) {
	_, err := bootstrap.NewInMemory(bootstrap.Options{TZDataVersion: "unknown", Now: time.Now, NewID: rand.Text, EventsPerSnapshot: 1})
	if !errors.Is(err, contract.ErrTimeZoneDataUnavailable) {
		t.Fatalf("false packaged version accepted=%v", err)
	}
}

func TestW5FreshReportCannotRelabelAnotherRuntime(t *testing.T) {
	s := contract.Scope{OrganizationID: "22222222-2222-4222-8222-222222222222", RestaurantID: "33333333-3333-4333-8333-333333333333", RuntimeEpoch: "44444444-4444-4444-8444-444444444444", LifecycleVersion: "1", TimeZone: "Europe/Moscow", SettingsVersion: "1"}
	r, err := bootstrap.NewInMemory(bootstrap.Options{Scope: s, Now: time.Now, NewID: rand.Text, EventsPerSnapshot: 1})
	if err != nil {
		t.Fatal(err)
	}
	request := contract.AnalyticsRequest{Scope: s, Actor: "11111111-1111-4111-8111-111111111111", ExpectedSettingsVersion: "1"}
	request.Scope.RestaurantID = "55555555-5555-4555-8555-555555555555"
	if _, err = r.AnalyticsStock(context.Background(), request); !errors.Is(err, contract.ErrForbidden) {
		t.Fatalf("foreign stock labelled with runtime data=%v", err)
	}
	if _, err = r.AnalyticsMovements(context.Background(), request); !errors.Is(err, contract.ErrForbidden) {
		t.Fatalf("foreign journal labelled with runtime data=%v", err)
	}
}
