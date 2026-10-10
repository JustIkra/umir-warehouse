package bootstrap

import "time"

type Options struct {
	Now               func() time.Time
	NewID             func() string
	EventsPerSnapshot int64
}
