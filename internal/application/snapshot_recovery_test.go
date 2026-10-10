package application

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/JustIkra/umir-warehouse/internal/adapters/memory"
	"github.com/JustIkra/umir-warehouse/internal/domain"
)

func TestSnapshotRecoveryMatchesFullReplayAndProcessesDueBatch(t *testing.T) {
	dueAt := moscowTime(2026, time.April, 2, 10)
	makeStore := func() *memory.EventStore {
		store := seedStore(t, dueAt, 10000, 0)
		at := moscowTime(2026, time.April, 1, 10)
		appendFixture(t, context.Background(), store, "register-extra", "RegisterProduct", domain.Event{EventID: "event-extra", RecordedAt: at, Type: domain.EventProductRegistered, Payload: domain.EventPayload{Product: &domain.Product{ProductID: "extra", Name: "Extra", BaseUnit: domain.BaseUnitG}}})
		return store
	}
	fullStore := makeStore()
	snapshotStore := makeStore()
	seedOwner, err := NewOwner(snapshotStore, func() time.Time { return moscowTime(2026, time.April, 1, 10) }, nextTestID())
	if err != nil {
		t.Fatal(err)
	}
	if err := seedOwner.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	candidate := snapshotFor(seedOwner.state)
	fullOwner, err := NewOwner(fullStore, func() time.Time { return dueAt }, nextTestID())
	if err != nil {
		t.Fatal(err)
	}
	fastOwner, err := NewOwner(snapshotStore, func() time.Time { return dueAt }, nextTestID())
	if err != nil {
		t.Fatal(err)
	}
	if err := fastOwner.ConfigureSnapshots(&fixedSnapshotStore{snapshot: candidate, found: true}, 2); err != nil {
		t.Fatal(err)
	}
	if _, err := fullOwner.Prepare(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := fastOwner.Prepare(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(fullOwner.state, fastOwner.state) {
		t.Fatalf("snapshot replay state differs from full replay\nfull: %#v\nsnapshot: %#v", fullOwner.state, fastOwner.state)
	}
	if len(fullOwner.expirations) != len(fastOwner.expirations) || len(fastOwner.expirations) != 0 {
		t.Fatalf("full/snapshot expiry indexes = %#v/%#v; want both empty after due processing", fullOwner.expirations, fastOwner.expirations)
	}
}

func TestSnapshotPrefixMismatchFallsBackToFullReplay(t *testing.T) {
	store := seedStore(t, moscowTime(2026, time.April, 4, 10), 10000, 0)
	seedOwner, err := NewOwner(store, func() time.Time { return moscowTime(2026, time.April, 1, 10) }, nextTestID())
	if err != nil {
		t.Fatal(err)
	}
	if err := seedOwner.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	candidate := snapshotFor(seedOwner.state)
	command := candidate.State.Commands["register-p"]
	command.CommandFingerprint = "different-journal-fingerprint"
	candidate.State.Commands["register-p"] = command
	if err := domain.ValidateSnapshot(candidate); err != nil {
		t.Fatalf("altered candidate should still be structurally valid: %v", err)
	}
	owner, err := NewOwner(store, func() time.Time { return moscowTime(2026, time.April, 1, 10) }, nextTestID())
	if err != nil {
		t.Fatal(err)
	}
	if err := owner.ConfigureSnapshots(&fixedSnapshotStore{snapshot: candidate, found: true}, 2); err != nil {
		t.Fatal(err)
	}
	if err := owner.Recover(context.Background()); err != nil {
		t.Fatalf("Recover() with a journal-mismatched candidate = %v; want full replay fallback", err)
	}
	if owner.state.StateVersion != 3 || owner.state.Commands["register-p"].CommandFingerprint == "different-journal-fingerprint" {
		t.Fatalf("recovered state did not come from journal: version %d command %#v", owner.state.StateVersion, owner.state.Commands["register-p"])
	}
}

func TestInvalidSnapshotCandidatesFallBackToFullReplay(t *testing.T) {
	store := seedStore(t, moscowTime(2026, time.April, 4, 10), 10000, 0)
	seedOwner, err := NewOwner(store, func() time.Time { return moscowTime(2026, time.April, 1, 10) }, nextTestID())
	if err != nil {
		t.Fatal(err)
	}
	if err := seedOwner.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	valid := snapshotFor(seedOwner.state)
	tests := []struct {
		name string
		edit func(*domain.Snapshot)
	}{
		{name: "schema", edit: func(s *domain.Snapshot) { s.SchemaVersion++ }},
		{name: "stream", edit: func(s *domain.Snapshot) { s.StreamID = "other" }},
		{name: "above journal", edit: func(s *domain.Snapshot) { s.LastSequence++; s.State.StateVersion++ }},
		{name: "non boundary", edit: func(s *domain.Snapshot) { s.LastSequence = 2; s.State.StateVersion = 2 }},
		{name: "broken reference", edit: func(s *domain.Snapshot) { delete(s.State.Products, "p") }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			candidate := domain.CloneSnapshot(valid)
			test.edit(&candidate)
			owner, err := NewOwner(store, func() time.Time { return moscowTime(2026, time.April, 1, 10) }, nextTestID())
			if err != nil {
				t.Fatal(err)
			}
			if err := owner.ConfigureSnapshots(&fixedSnapshotStore{snapshot: candidate, found: true}, 2); err != nil {
				t.Fatal(err)
			}
			if err := owner.Recover(context.Background()); err != nil {
				t.Fatalf("Recover() with unusable candidate = %v; want full replay fallback", err)
			}
			if owner.state.StateVersion != 3 || owner.state.Products["p"].ProductID != "p" {
				t.Fatalf("recovered state = version %d, product %#v; want journal version 3", owner.state.StateVersion, owner.state.Products["p"])
			}
		})
	}
}

func TestRecoveryRejectsSequenceGapAndUnknownEventSchema(t *testing.T) {
	for _, test := range []struct {
		name string
		edit func([]domain.RecordedBatch)
		want error
	}{
		{name: "gap", edit: func(batches []domain.RecordedBatch) { batches[0].Events[0].Sequence++ }, want: domain.ErrInvalidRecordedBatch},
		{name: "schema", edit: func(batches []domain.RecordedBatch) { batches[0].Events[0].SchemaVersion++ }, want: domain.ErrUnsupportedSchema},
	} {
		t.Run(test.name, func(t *testing.T) {
			base := seedStore(t, moscowTime(2026, time.April, 4, 10), 10000, 0)
			owner, err := NewOwner(&editedBatchesStore{EventStore: base, edit: test.edit}, func() time.Time { return moscowTime(2026, time.April, 1, 10) }, nextTestID())
			if err != nil {
				t.Fatal(err)
			}
			if err := owner.Recover(context.Background()); err == nil || !errors.Is(err, test.want) {
				t.Fatalf("Recover() error = %v; want %v", err, test.want)
			}
		})
	}
}

func TestConfigureSnapshotsRequiresEarlyPositiveStore(t *testing.T) {
	store := memory.NewEventStore()
	owner, err := NewOwner(store, time.Now, nextTestID())
	if err != nil {
		t.Fatal(err)
	}
	if err := owner.ConfigureSnapshots(nil, 1); err == nil {
		t.Fatal("ConfigureSnapshots(nil, 1) succeeded")
	}
	if err := owner.ConfigureSnapshots(memory.NewSnapshotStore(), 0); err == nil {
		t.Fatal("ConfigureSnapshots(store, 0) succeeded")
	}
	if err := owner.ConfigureSnapshots(memory.NewSnapshotStore(), 1); err != nil {
		t.Fatalf("configure before recovery: %v", err)
	}
	if _, err := owner.Prepare(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := owner.ConfigureSnapshots(memory.NewSnapshotStore(), 1); err == nil {
		t.Fatal("ConfigureSnapshots after first Prepare succeeded")
	}
}

func snapshotFor(state domain.InventoryState) domain.Snapshot {
	return domain.Snapshot{SchemaVersion: domain.InventorySchemaV1, StreamID: domain.InventoryStreamID, LastSequence: state.StateVersion, State: domain.CloneInventoryState(state)}
}

type fixedSnapshotStore struct {
	snapshot domain.Snapshot
	found    bool
}

func (s *fixedSnapshotStore) LoadLatest(context.Context) (domain.Snapshot, bool, error) {
	return domain.CloneSnapshot(s.snapshot), s.found, nil
}

func (s *fixedSnapshotStore) Save(context.Context, domain.Snapshot) error { return nil }

type editedBatchesStore struct {
	EventStore
	edit func([]domain.RecordedBatch)
}

func (s *editedBatchesStore) ReadBatches(ctx context.Context, sequence int64) ([]domain.RecordedBatch, error) {
	batches, err := s.EventStore.ReadBatches(ctx, sequence)
	if err == nil {
		s.edit(batches)
	}
	return batches, err
}
