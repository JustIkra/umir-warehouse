package contract

import (
	"errors"
	"time"
)

var (
	ErrCalculationUnavailable  = errors.New("analytics calculation unavailable")
	ErrSettingsChanged         = errors.New("organization settings changed")
	ErrUnknownTimeZone         = errors.New("unknown IANA time zone")
	ErrTimeZoneDataUnavailable = errors.New("time zone data unavailable")
	ErrAmbiguousLocalTime      = errors.New("ambiguous local time")
	ErrNonexistentLocalTime    = errors.New("nonexistent local time")
	ErrSkippedDate             = errors.New("skipped calendar date")
	ErrRefreshRequired         = errors.New("report refresh required")
	ErrUnknownEventSchema      = errors.New("unknown event schema")
	ErrLimitExceeded           = errors.New("report capacity exceeded")
)

type AnalyticsRequest struct {
	Scope                     Scope
	Actor                     string
	ExpectedSettingsVersion   string
	ProductID, Search, Cursor string
	Limit, RiskHours          int
	FromDate, ThroughDate     string
}
type ReportIdentity struct {
	Scope                        Scope
	EvaluatedAt                  time.Time
	StateVersion, PrefixSequence int64
	FilterFingerprint            string
}
type StockRow struct {
	ProductID, Name                                                              string
	BaseUnit                                                                     BaseUnit
	AvailableQuantity, PendingWriteoffQuantity, OnHandQuantity, ExpiringQuantity Quantity
}
type StockReport struct {
	Identity             ReportIdentity
	RiskHours            int
	RiskCutoff           time.Time
	Items                []StockRow
	NextCursor           string
	InvalidationRequired bool
}
type MovementRow struct {
	ProductID, Name                                       string
	BaseUnit                                              BaseUnit
	ReceivedQuantity, OrderedQuantity, WrittenOffQuantity Quantity
}
type Coverage struct {
	Epoch                        string
	CoverageFrom                 time.Time
	Complete                     bool
	RequestedFrom, AvailableFrom time.Time
}
type MovementReport struct {
	Identity   ReportIdentity
	From, To   time.Time
	Coverage   Coverage
	Items      []MovementRow
	NextCursor string
}

type TimeInput struct {
	Instant string
	Civil   *CivilInput
}
type CivilInput struct {
	LocalDateTime string
	Offset        string
	Occurrence    string
}
type DeliveryTimeInputs struct {
	CreatedAt TimeInput
	Lines     []DeliveryLineTimes
}
type DeliveryLineTimes struct{ ManufacturedAt, ReceivedAt TimeInput }
