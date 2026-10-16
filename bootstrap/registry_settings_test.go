package bootstrap

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/JustIkra/umir-warehouse/contract"
	"github.com/JustIkra/umir-warehouse/internal/application"
	"github.com/JustIkra/umir-warehouse/internal/domain"
)

const (
	settingsOrg    = "22222222-2222-4222-8222-222222222222"
	settingsTenant = "33333333-3333-4333-8333-333333333333"
	settingsEpoch  = "44444444-4444-4444-8444-444444444444"
)

func settingsScope(zone, version string) contract.Scope {
	return contract.Scope{OrganizationID: settingsOrg, RestaurantID: settingsTenant, RuntimeEpoch: settingsEpoch, LifecycleVersion: "1", TimeZone: zone, SettingsVersion: version}
}

func settingsRegistry(t *testing.T) *Registry {
	t.Helper()
	now := time.Date(2026, 10, 10, 12, 0, 0, 0, time.FixedZone("MSK", 3*60*60))
	registry, err := NewRegistry(Options{Now: func() time.Time { return now }, NewID: func() string { return "id" }, EventsPerSnapshot: 10}, settingsEpoch)
	if err != nil {
		t.Fatal(err)
	}
	return registry
}

// currentSettings is what the restaurant's runtime serves under now.
func currentSettings(t *testing.T, registry *Registry) contract.Scope {
	t.Helper()
	runtime, err := registry.Resolve(context.Background(), settingsScope("Europe/Moscow", "1"))
	if err != nil {
		t.Fatal(err)
	}
	run, err := runtime.RunExpirations(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return run.Scope
}

func TestRepeatedRegistrationWithNewerSettingsReplacesThem(t *testing.T) {
	ctx := context.Background()
	registry := settingsRegistry(t)
	if err := registry.Register(ctx, settingsScope("Europe/Moscow", "1")); err != nil {
		t.Fatal(err)
	}
	runtime, err := registry.Resolve(ctx, settingsScope("Europe/Moscow", "1"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.RegisterProduct(ctx, contract.RegisterProductRequest{CommandID: "p", ProductID: "p", Name: "Milk", BaseUnit: contract.BaseUnitML}); err != nil {
		t.Fatal(err)
	}
	before, err := runtime.GetHistory(ctx)
	if err != nil {
		t.Fatal(err)
	}

	if err := registry.Register(ctx, settingsScope("Asia/Yekaterinburg", "10")); err != nil {
		t.Fatalf("Register() with newer settings = %v", err)
	}
	got := currentSettings(t, registry)
	if got.TimeZone != "Asia/Yekaterinburg" || got.SettingsVersion != "10" {
		t.Fatalf("settings after the repeat = %s/%s; want Asia/Yekaterinburg/10", got.TimeZone, got.SettingsVersion)
	}
	// An older repeat (version 2 < 10) succeeds and changes nothing, even with another zone.
	if err := registry.Register(ctx, settingsScope("Europe/Moscow", "2")); err != nil {
		t.Fatalf("Register() with older settings = %v", err)
	}
	if got := currentSettings(t, registry); got.TimeZone != "Asia/Yekaterinburg" || got.SettingsVersion != "10" {
		t.Fatalf("an older repeat changed the settings to %s/%s", got.TimeZone, got.SettingsVersion)
	}
	// The same version with another zone is a different request.
	if err := registry.Register(ctx, settingsScope("Europe/Moscow", "10")); !errors.Is(err, contract.ErrCommandIdentityConflict) {
		t.Fatalf("Register() with the same version and another zone = %v; want ErrCommandIdentityConflict", err)
	}
	// The same settings repeat as before.
	if err := registry.Register(ctx, settingsScope("Asia/Yekaterinburg", "10")); err != nil {
		t.Fatalf("exact repeat = %v", err)
	}
	after, err := runtime.GetHistory(ctx)
	if err != nil || after.StateVersion != before.StateVersion || len(after.Events) != len(before.Events) {
		t.Fatalf("journal after the repeats = %+v, %v; want it unchanged", after, err)
	}
}

// Auth repeats an operation whose answer was lost; by then the organization's
// settings have moved on, and the repeat carries the new ones.
func TestLostRegistrationAnswerRepeatedWithNewSettings(t *testing.T) {
	ctx := context.Background()
	registry := settingsRegistry(t)
	const operation = "55555555-5555-4555-8555-555555555555"
	if _, err := registry.RegisterRuntime(ctx, settingsScope("Europe/Moscow", "1"), operation); err != nil {
		t.Fatal(err)
	}
	readiness, err := registry.RegisterRuntime(ctx, settingsScope("Europe/Samara", "3"), operation)
	if err != nil || readiness.State != "ready" {
		t.Fatalf("repeated operation = %+v, %v; want ready", readiness, err)
	}
	if got := currentSettings(t, registry); got.TimeZone != "Europe/Samara" || got.SettingsVersion != "3" {
		t.Fatalf("settings = %s/%s; want Europe/Samara/3", got.TimeZone, got.SettingsVersion)
	}
	if _, err := registry.RegisterRuntime(ctx, settingsScope("Europe/Samara", "3"), "66666666-6666-4666-8666-666666666666"); !errors.Is(err, contract.ErrCommandIdentityConflict) {
		t.Fatalf("another operation = %v; want ErrCommandIdentityConflict", err)
	}
}

// gateStore holds appends until released, which keeps the owner busy.
type gateStore struct {
	application.EventStore
	armed   atomic.Bool
	entered chan struct{}
	release chan struct{}
}

func (s *gateStore) AppendBatch(ctx context.Context, batch domain.CommandBatch) (domain.AppendResult, error) {
	if s.armed.Load() {
		close(s.entered)
		<-s.release
	}
	return s.EventStore.AppendBatch(ctx, batch)
}

// A settings update waits for the restaurant's owner; that wait must not hold
// up the registry, so another restaurant can be registered meanwhile.
func TestSettingsUpdateWaitDoesNotBlockRegistry(t *testing.T) {
	ctx := context.Background()
	var gates []*gateStore
	now := time.Date(2026, 10, 10, 12, 0, 0, 0, time.FixedZone("MSK", 3*60*60))
	registry, err := NewRegistry(Options{Now: func() time.Time { return now }, NewID: func() string { return "id" }, EventsPerSnapshot: 10,
		WrapEventStore: func(inner application.EventStore) application.EventStore {
			gate := &gateStore{EventStore: inner, entered: make(chan struct{}), release: make(chan struct{})}
			gates = append(gates, gate)
			return gate
		}}, settingsEpoch)
	if err != nil {
		t.Fatal(err)
	}
	if err := registry.Register(ctx, settingsScope("Europe/Moscow", "1")); err != nil {
		t.Fatal(err)
	}
	runtime, err := registry.Resolve(ctx, settingsScope("Europe/Moscow", "1"))
	if err != nil {
		t.Fatal(err)
	}
	gate := gates[0]
	gate.armed.Store(true)
	busy := make(chan error, 1)
	go func() {
		_, err := runtime.RegisterProduct(ctx, contract.RegisterProductRequest{CommandID: "p", ProductID: "p", Name: "Milk", BaseUnit: contract.BaseUnitML})
		busy <- err
	}()
	<-gate.entered
	updated := make(chan error, 1)
	go func() { updated <- registry.Register(ctx, settingsScope("Europe/Samara", "2")) }()
	time.Sleep(50 * time.Millisecond) // the update is now waiting for the owner, or about to
	other := settingsScope("Europe/Moscow", "1")
	other.RestaurantID = "33333333-3333-4333-8333-333333333334"
	registered := make(chan error, 1)
	go func() { registered <- registry.Register(ctx, other) }()
	select {
	case err := <-registered:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		close(gate.release)
		t.Fatal("a busy owner held up the registration of another restaurant")
	}
	close(gate.release)
	if err := <-busy; err != nil {
		t.Fatal(err)
	}
	if err := <-updated; err != nil {
		t.Fatalf("the update after the owner was free = %v", err)
	}
	if got := currentSettings(t, registry); got.TimeZone != "Europe/Samara" || got.SettingsVersion != "2" {
		t.Fatalf("settings = %s/%s; want Europe/Samara/2", got.TimeZone, got.SettingsVersion)
	}
}
