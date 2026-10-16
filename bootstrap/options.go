package bootstrap

import (
	"time"

	"github.com/JustIkra/umir-warehouse/contract"
	"github.com/JustIkra/umir-warehouse/internal/application"
)

type Options struct {
	CoverageFrom      time.Time
	Scope             contract.Scope
	TZDataVersion     string
	Now               func() time.Time
	PhysicalNow       func() time.Time
	NewID             func() string
	EventsPerSnapshot int64
	// WrapEventStore, when set, wraps the event store of each restaurant. Tests
	// use it to observe or fail store calls.
	WrapEventStore func(application.EventStore) application.EventStore
	// WrapSnapshotStore does the same for the snapshot store of a restaurant.
	WrapSnapshotStore func(contract.Scope, application.SnapshotStore) application.SnapshotStore
}

type HTTPOptions struct {
	RequestTimeout  time.Duration
	MaxRequestBytes int64
}
