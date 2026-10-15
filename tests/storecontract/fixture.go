// Package storecontract holds the behavior every EventStore and SnapshotStore
// backend must provide. Backends call RunEventStore and RunSnapshotStore from
// their own tests with a Fixture for that backend.
package storecontract

import (
	"testing"

	"github.com/JustIkra/umir-warehouse/internal/application"
)

type Stores struct {
	Events    application.EventStore
	Snapshots application.SnapshotStore
}

type Fixture struct {
	// New returns stores of an empty restaurant.
	New func(t *testing.T) Stores
	// Other returns stores of a second restaurant on the same backend as s.
	Other func(t *testing.T, s Stores) Stores
	// Close releases stores returned by New or Other. It may be nil.
	Close func(t *testing.T, s Stores)
}

func (f Fixture) open(t *testing.T) Stores {
	t.Helper()
	s := f.New(t)
	f.closeLater(t, s)
	return s
}

func (f Fixture) openOther(t *testing.T, s Stores) Stores {
	t.Helper()
	other := f.Other(t, s)
	f.closeLater(t, other)
	return other
}

func (f Fixture) closeLater(t *testing.T, s Stores) {
	t.Helper()
	if f.Close != nil {
		t.Cleanup(func() { f.Close(t, s) })
	}
}
