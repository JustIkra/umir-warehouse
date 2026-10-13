package bootstrap

import (
	"time"

	"github.com/JustIkra/umir-warehouse/contract"
)

type Options struct {
	CoverageFrom      time.Time
	Scope             contract.Scope
	TZDataVersion     string
	Now               func() time.Time
	NewID             func() string
	EventsPerSnapshot int64
}

type HTTPOptions struct {
	RequestTimeout  time.Duration
	MaxRequestBytes int64
}
