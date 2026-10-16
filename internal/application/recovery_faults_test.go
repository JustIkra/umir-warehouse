package application

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/JustIkra/umir-warehouse/internal/adapters/memory"
	"github.com/JustIkra/umir-warehouse/internal/domain"
)

// logSink collects what an Owner logs, safe for the snapshot worker's goroutine.
type logSink struct {
	mu    sync.Mutex
	lines []string
	wake  chan struct{}
}

func newLogSink() *logSink { return &logSink{wake: make(chan struct{}, 16)} }

func (l *logSink) logf(format string, args ...any) {
	l.mu.Lock()
	l.lines = append(l.lines, strings.TrimSpace(fmt.Sprintf(format, args...)))
	l.mu.Unlock()
	select {
	case l.wake <- struct{}{}:
	default:
	}
}

func (l *logSink) text() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return strings.Join(l.lines, "\n")
}

// waitFor blocks until the log holds the text or the timeout passes.
func (l *logSink) waitFor(t *testing.T, text string) {
	t.Helper()
	deadline := time.After(10 * time.Second)
	for !strings.Contains(l.text(), text) {
		select {
		case <-l.wake:
		case <-deadline:
			t.Fatalf("log never held %q: %q", text, l.text())
		}
	}
}

// savedSnapshots returns a snapshot store holding the snapshot an owner takes
// of the journal in store.
func savedSnapshots(t *testing.T, ctx context.Context, store EventStore, at time.Time) *memory.SnapshotStore {
	t.Helper()
	snapshots := memory.NewSnapshotStore()
	seed, err := NewOwner(store, func() time.Time { return at }, nextTestID())
	if err != nil {
		t.Fatal(err)
	}
	if err := seed.ConfigureSnapshots(snapshots, 2); err != nil {
		t.Fatal(err)
	}
	if err := seed.Recover(ctx); err != nil {
		t.Fatal(err)
	}
	if worked, err := seed.RunSnapshotOnce(ctx); err != nil || !worked {
		t.Fatalf("RunSnapshotOnce() = %v, %v; want a saved snapshot", worked, err)
	}
	return snapshots
}

func TestJournalWalkRejectsRepeatedIDs(t *testing.T) {
	ctx := context.Background()
	store := seedStore(t, moscowTime(2026, time.April, 4, 10), 10000, 0)
	batches, err := store.ReadBatches(ctx, 0)
	if err != nil {
		t.Fatal(err)
	}
	// second repeats the next batch with another command or event ID.
	second := func(edit func(*domain.RecordedBatch)) domain.RecordedBatch {
		batch := batches[1]
		batch.Events = append([]domain.Event(nil), batch.Events...)
		edit(&batch)
		return batch
	}
	withCommand := func(id string) func(*domain.RecordedBatch) {
		return func(b *domain.RecordedBatch) {
			b.CommandID = id
			for i := range b.Events {
				b.Events[i].CommandID = id
			}
		}
	}
	withEvent := func(id string) func(*domain.RecordedBatch) {
		return func(b *domain.RecordedBatch) { b.Events[0].EventID = id }
	}
	for _, test := range []struct {
		name string
		edit func(*domain.RecordedBatch)
		from int64
		want error
	}{
		{"fresh IDs pass", withCommand("fresh"), 0, nil},
		{"repeated command ID", withCommand(batches[0].CommandID), 0, domain.ErrInvalidRecordedBatch},
		{"repeated event ID", withEvent(batches[0].Events[0].EventID), 0, domain.ErrInvalidRecordedBatch},
		// Only a walk from the start sees IDs of the part it skipped; the store
		// guards them on write.
		{"repeated command ID after a snapshot", withCommand(batches[0].CommandID), 1, nil},
		{"repeated event ID after a snapshot", withEvent(batches[0].Events[0].EventID), 1, nil},
	} {
		t.Run(test.name, func(t *testing.T) {
			walk := newJournalWalk(0)
			if err := walk.check(batches[0]); err != nil {
				t.Fatal(err)
			}
			if test.from > 0 {
				walk = newJournalWalk(test.from)
			}
			if err := walk.check(second(test.edit)); !errors.Is(err, test.want) && (test.want != nil || err != nil) {
				t.Fatalf("check() = %v; want %v", err, test.want)
			}
		})
	}
}

func TestSnapshotTailIOFailureIsNotAJournalFault(t *testing.T) {
	ctx := context.Background()
	at := moscowTime(2026, time.April, 1, 10)
	base := seedStore(t, moscowTime(2026, time.April, 4, 10), 10000, 0)
	snapshots := savedSnapshots(t, ctx, base, at)
	counting := &journalCountingStore{EventStore: base}
	sink := newLogSink()
	owner, err := NewOwner(counting, func() time.Time { return at }, nextTestID())
	if err != nil {
		t.Fatal(err)
	}
	if err := owner.ConfigureLogger(sink.logf); err != nil {
		t.Fatal(err)
	}
	if err := owner.ConfigureSnapshots(snapshots, 2); err != nil {
		t.Fatal(err)
	}
	boom := errors.New("disk unavailable")
	counting.setScanErr(func(int64) error { return boom })

	if err := owner.Recover(ctx); !errors.Is(err, boom) {
		t.Fatalf("Recover() = %v; want the I/O error", err)
	}
	if reads := counting.readsFromStart(); reads != 0 {
		t.Fatalf("an I/O failure sent recovery to read the journal from the start %d times; want 0", reads)
	}
	if strings.Contains(sink.text(), "unusable") {
		t.Fatalf("an I/O failure was logged as an unusable snapshot: %q", sink.text())
	}
}

func TestJournalFaultMarksProjectionUnusable(t *testing.T) {
	ctx := context.Background()
	at := moscowTime(2026, time.April, 1, 10)
	counting := &journalCountingStore{EventStore: seedStore(t, moscowTime(2026, time.April, 4, 10), 10000, 0)}
	owner, err := NewOwner(counting, func() time.Time { return at }, nextTestID())
	if err != nil {
		t.Fatal(err)
	}
	if err := owner.Recover(ctx); err != nil {
		t.Fatal(err)
	}
	counting.setScanErr(func(int64) error { return domain.ErrInvalidRecordedBatch })
	owner.mu.lockUncancellable()
	err = owner.recoverLocked(ctx)
	usable := owner.usable
	owner.mu.Unlock()
	if !errors.Is(err, domain.ErrInvalidRecordedBatch) || usable {
		t.Fatalf("recoverLocked() = %v with usable %v; want the journal fault and an unusable projection", err, usable)
	}
	counting.setScanErr(nil)
	if err := owner.Recover(ctx); err != nil {
		t.Fatalf("Recover() after the journal is readable = %v", err)
	}
	if !owner.usable {
		t.Fatal("a good rebuild left the projection unusable")
	}
}

// failingFindStore fails FindCommand and counts the journal scans.
type failingFindStore struct {
	EventStore
	mu    sync.Mutex
	scans int
	err   error
}

func (s *failingFindStore) FindCommand(context.Context, string) (domain.CommandRecord, bool, error) {
	return domain.CommandRecord{}, false, s.err
}

func (s *failingFindStore) ScanBatches(ctx context.Context, after int64, fn func(domain.RecordedBatch) error) error {
	s.mu.Lock()
	s.scans++
	s.mu.Unlock()
	return s.EventStore.ScanBatches(ctx, after, fn)
}

func TestRecoveryLooksUpPendingCommandBeforeReadingJournal(t *testing.T) {
	ctx := context.Background()
	at := moscowTime(2026, time.April, 1, 10)
	store := &failingFindStore{EventStore: seedStore(t, moscowTime(2026, time.April, 4, 10), 10000, 0), err: errors.New("lookup unavailable")}
	owner, err := NewOwner(store, func() time.Time { return at }, nextTestID())
	if err != nil {
		t.Fatal(err)
	}
	owner.pending = &domain.CommandBatch{CommandID: "pending", CommandType: "ConfirmOrder", CommandFingerprint: "fp"}
	owner.mu.lockUncancellable()
	err = owner.recoverLocked(ctx)
	owner.mu.Unlock()
	if !errors.Is(err, store.err) {
		t.Fatalf("recoverLocked() = %v; want the lookup error", err)
	}
	if store.scans != 0 {
		t.Fatalf("recovery scanned the journal %d times before the pending command was looked up; want 0", store.scans)
	}
}
