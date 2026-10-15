//go:build perf

package perf

import (
	"context"
	"crypto/rand"
	"fmt"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/JustIkra/umir-warehouse/contract"
	"github.com/JustIkra/umir-warehouse/internal/adapters/memory"
	"github.com/JustIkra/umir-warehouse/internal/adapters/zonedata"
	"github.com/JustIkra/umir-warehouse/internal/application"
)

const snapshotEvery = 100

type clock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *clock) Advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	c.mu.Unlock()
}

func newUUID() string {
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		panic(err)
	}
	id[6] = (id[6] & 15) | 64
	id[8] = (id[8] & 63) | 128
	return fmt.Sprintf("%x-%x-%x-%x-%x", id[:4], id[4:6], id[6:8], id[8:10], id[10:])
}

// heapMiB is the live heap after two collections.
func heapMiB() float64 {
	runtime.GC()
	runtime.GC()
	var stats runtime.MemStats
	runtime.ReadMemStats(&stats)
	return float64(stats.HeapAlloc) / (1 << 20)
}

// stand is one restaurant over a memory journal and snapshot store, built the
// way bootstrap.NewInMemory builds it but without the HTTP layer.
type stand struct {
	t         *testing.T
	ctx       context.Context
	clock     *clock
	scope     contract.Scope
	store     *memory.EventStore
	snapshots *memory.SnapshotStore
	owner     *application.Owner
	planning  *application.Planning
	analytics *application.Analytics
	// deliveries counts received deliveries; each creates one batch per product.
	deliveries int
}

func newStand(t *testing.T) *stand {
	t.Helper()
	if err := zonedata.Check(); err != nil {
		t.Fatal(err)
	}
	msk := time.FixedZone("MSK", 3*3600)
	s := &stand{
		t:         t,
		ctx:       context.Background(),
		clock:     &clock{now: time.Date(2026, 1, 10, 9, 0, 0, 0, msk)},
		scope:     contract.Scope{OrganizationID: newUUID(), RestaurantID: newUUID(), RuntimeEpoch: newUUID(), LifecycleVersion: "1", TimeZone: "Europe/Moscow", SettingsVersion: "1"},
		store:     memory.NewEventStore(),
		snapshots: memory.NewSnapshotStore(),
	}
	s.owner = s.newOwner(s.snapshots)
	coverage := s.clock.Now()
	var err error
	if s.planning, err = application.NewPlanning(s.owner, coverage); err != nil {
		t.Fatal(err)
	}
	s.analytics = newAnalytics(t, s.owner, coverage, s.clock.Now)
	return s
}

// newOwner builds an owner over the stand's journal; snapshots may be nil.
func (s *stand) newOwner(snapshots application.SnapshotStore) *application.Owner {
	s.t.Helper()
	owner, err := application.NewOwner(s.store, s.clock.Now, newUUID)
	if err != nil {
		s.t.Fatal(err)
	}
	for _, configure := range []func() error{
		func() error { return owner.ConfigureZoneLoader(zonedata.Load) },
		func() error { return owner.ConfigureTZData(zonedata.Version) },
		func() error { return owner.ConfigureScope(s.scope) },
	} {
		if err := configure(); err != nil {
			s.t.Fatal(err)
		}
	}
	if snapshots != nil {
		if err := owner.ConfigureSnapshots(snapshots, snapshotEvery); err != nil {
			s.t.Fatal(err)
		}
	}
	return owner
}

type stockReader struct{ owner *application.Owner }

func (r stockReader) PrepareStock(ctx context.Context) (contract.StockPreparation, error) {
	return r.owner.PrepareStock(ctx)
}

func (r stockReader) CalculateStock(ctx context.Context, prepared contract.StockPreparation) (contract.InventoryView, error) {
	return application.CalculateStock(ctx, prepared)
}

// newAnalytics is the one place that wires Analytics to the owner; the journal
// reader port is the part that changes when analytics moves to bounded reads.
func newAnalytics(t *testing.T, owner *application.Owner, coverage time.Time, now func() time.Time) *application.Analytics {
	t.Helper()
	analytics, err := application.NewAnalytics(stockReader{owner}, owner, coverage, now, rand.Text, zonedata.Load)
	if err != nil {
		t.Fatal(err)
	}
	return analytics
}

// flushSnapshots saves every queued snapshot so a later restart finds it.
func (s *stand) flushSnapshots() {
	s.t.Helper()
	for {
		worked, err := s.owner.RunSnapshotOnce(s.ctx)
		if err != nil {
			s.t.Fatalf("save snapshot: %v", err)
		}
		if !worked {
			return
		}
	}
}
