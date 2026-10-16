package application

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"maps"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"golang.org/x/text/cases"
	"golang.org/x/text/unicode/norm"

	"github.com/JustIkra/umir-warehouse/contract"
	"github.com/JustIkra/umir-warehouse/internal/domain"
)

type AnalyticsStockReader interface {
	PrepareStock(context.Context) (contract.StockPreparation, error)
	CalculateStock(context.Context, contract.StockPreparation) (contract.InventoryView, error)
}

// JournalCapture is what a report fixes about the journal: how far it had
// gone, when, and the names of the products.
type JournalCapture struct {
	Version  int64
	At       time.Time
	Products map[string]domain.Product
}

// AnalyticsJournalReader captures the journal under the owner's lock and then
// reads the events of a period without it.
type AnalyticsJournalReader interface {
	CaptureJournal(context.Context) (JournalCapture, error)
	ReadPeriod(context.Context, PeriodQuery) ([]domain.Event, error)
}
type capturedReport struct {
	request  contract.AnalyticsRequest
	expires  time.Time
	stock    *contract.StockReport
	movement *contract.MovementReport
	pages    map[int]string
}
type reportCursor struct {
	report *capturedReport
	index  int
}
type Analytics struct {
	mu        sync.Mutex
	loadZone  func(string) (*time.Location, error)
	stock     AnalyticsStockReader
	journal   AnalyticsJournalReader
	coverage  time.Time
	now       func() time.Time
	newCursor func() string
	reports   map[*capturedReport]bool
	cursors   map[string]reportCursor
}

func NewAnalytics(stock AnalyticsStockReader, journal AnalyticsJournalReader, coverage time.Time, now func() time.Time, newCursor func() string, loadZone func(string) (*time.Location, error)) (*Analytics, error) {
	if loadZone == nil || stock == nil || journal == nil || coverage.IsZero() || now == nil || newCursor == nil {
		return nil, contract.ErrInvalidArgument
	}
	return &Analytics{loadZone: loadZone, stock: stock, journal: journal, coverage: coverage, now: now, newCursor: newCursor, reports: map[*capturedReport]bool{}, cursors: map[string]reportCursor{}}, nil
}

// CaptureJournal brings the projection up to date and returns its version, the
// clock and a copy of the products.
func (o *Owner) CaptureJournal(ctx context.Context) (JournalCapture, error) {
	if err := o.mu.Lock(ctx); err != nil {
		return JournalCapture{}, inventoryUnavailable(err)
	}
	defer o.mu.Unlock()
	if err := o.syncProjectionLocked(ctx); err != nil {
		return JournalCapture{}, inventoryUnavailable(err)
	}
	return JournalCapture{Version: o.state.StateVersion, At: domain.CanonicalBusinessTime(o.now()), Products: maps.Clone(o.state.Products)}, nil
}

// ReadPeriod reads the events of a period from the journal.
func (o *Owner) ReadPeriod(ctx context.Context, query PeriodQuery) ([]domain.Event, error) {
	return readPeriod(ctx, o.events, query)
}
func normalizeAnalytics(r contract.AnalyticsRequest, stock bool) (contract.AnalyticsRequest, error) {
	if r.Scope.Validate() != nil || r.Actor == "" || !contract.ValidVersion(r.ExpectedSettingsVersion) || !utf8.ValidString(r.Search) || utf8.RuneCountInString(r.Search) > 100 || !utf8.ValidString(r.ProductID) {
		return r, contract.ErrInvalidArgument
	}
	r.Search = norm.NFC.String(cases.Fold().String(norm.NFC.String(r.Search)))
	if r.Limit == 0 {
		r.Limit = 100
	}
	if r.Limit < 1 || r.Limit > 500 {
		return r, contract.ErrInvalidArgument
	}
	if stock {
		if r.FromDate != "" || r.ThroughDate != "" {
			return r, contract.ErrInvalidArgument
		}
		if r.RiskHours == 0 {
			r.RiskHours = 48
		}
		if r.RiskHours < 1 || r.RiskHours > 8784 {
			return r, contract.ErrInvalidArgument
		}
	} else if r.RiskHours != 0 || (r.FromDate == "") != (r.ThroughDate == "") {
		return r, contract.ErrInvalidArgument
	}
	return r, nil
}
func analyticsFingerprint(r contract.AnalyticsRequest) string {
	sum := sha256.Sum256([]byte(fingerprintJSON("AnalyticsFilters", struct {
		ProductID, Search, FromDate, ThroughDate string
		Limit, RiskHours                         int
	}{r.ProductID, r.Search, r.FromDate, r.ThroughDate, r.Limit, r.RiskHours})))
	return hex.EncodeToString(sum[:])
}
func analyticsMatch(id, name string, r contract.AnalyticsRequest) bool {
	return (r.ProductID == "" || id == r.ProductID) && (r.Search == "" || strings.Contains(norm.NFC.String(cases.Fold().String(norm.NFC.String(name))), r.Search))
}
func (a *Analytics) fresh(r contract.AnalyticsRequest) error {
	if r.ExpectedSettingsVersion != r.Scope.SettingsVersion {
		return contract.ErrSettingsChanged
	}
	_, err := a.loadZone(r.Scope.TimeZone)
	if err != nil {
		return calendarError(err)
	}
	return nil
}
func (a *Analytics) Stock(ctx context.Context, request contract.AnalyticsRequest) (contract.StockReport, error) {
	return a.StockUsing(ctx, request, a.stock)
}

func (a *Analytics) StockUsing(ctx context.Context, request contract.AnalyticsRequest, reader AnalyticsStockReader) (contract.StockReport, error) {
	if err := ctx.Err(); err != nil {
		return contract.StockReport{}, err
	}
	if reader == nil {
		return contract.StockReport{}, contract.ErrInvalidArgument
	}
	r, err := normalizeAnalytics(request, true)
	if err != nil {
		return contract.StockReport{}, err
	}
	if r.Cursor != "" {
		return a.stockPage(r)
	}
	if err := a.fresh(r); err != nil {
		return contract.StockReport{}, err
	}
	prepared, err := reader.PrepareStock(ctx)
	result := contract.StockReport{Identity: contract.ReportIdentity{Scope: r.Scope, EvaluatedAt: prepared.EvaluatedAt, StateVersion: prepared.StateVersion, PrefixSequence: prepared.StateVersion, FilterFingerprint: analyticsFingerprint(r)}, RiskHours: r.RiskHours, InvalidationRequired: prepared.InvalidationRequired}
	if err != nil {
		return result, err
	}
	inventory, err := reader.CalculateStock(ctx, prepared)
	if err != nil {
		return result, errors.Join(contract.ErrCalculationUnavailable, err)
	}
	result.RiskCutoff = prepared.EvaluatedAt.Add(time.Duration(r.RiskHours) * time.Hour)
	result.Items = make([]contract.StockRow, 0, len(inventory.Products))
	for _, p := range inventory.Products {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		if !analyticsMatch(p.ProductID, p.Name, r) {
			continue
		}
		row := contract.StockRow{ProductID: p.ProductID, Name: p.Name, BaseUnit: p.BaseUnit, OnHandQuantity: p.Quantity, AvailableQuantity: p.AvailableQuantity, PendingWriteoffQuantity: p.PendingWriteoffQuantity}
		for _, b := range p.Batches {
			if !b.PendingWriteoff && !b.ExpiresAt.After(result.RiskCutoff) {
				q, e := domain.AddQuantity(domain.Quantity(row.ExpiringQuantity), domain.Quantity(b.RemainingQuantity))
				if e != nil {
					return result, inventoryUnavailable(e)
				}
				row.ExpiringQuantity = contract.Quantity(q)
			}
		}
		result.Items = append(result.Items, row)
	}
	sort.Slice(result.Items, func(i, j int) bool { return result.Items[i].ProductID < result.Items[j].ProductID })
	return a.captureStock(r, result)
}
func (a *Analytics) movementPeriod(r contract.AnalyticsRequest, at time.Time) (time.Time, time.Time, error) {
	zone, err := a.loadZone(r.Scope.TimeZone)
	if err != nil {
		return time.Time{}, time.Time{}, calendarError(err)
	}
	from, through := r.FromDate, r.ThroughDate
	if from == "" {
		local := at.In(zone)
		y, m, d := local.Date()
		today := time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
		from = today.AddDate(0, 0, -6).Format("2006-01-02")
		through = today.Format("2006-01-02")
	}
	first, e1 := time.Parse("2006-01-02", from)
	last, e2 := time.Parse("2006-01-02", through)
	if e1 != nil || e2 != nil || last.Before(first) || last.Sub(first)/(24*time.Hour) >= 366 {
		return time.Time{}, time.Time{}, contract.ErrInvalidArgument
	}
	start, err := domain.DateBoundary(from, zone)
	if err != nil {
		return time.Time{}, time.Time{}, calendarError(err)
	}
	if _, err = domain.DateBoundary(through, zone); err != nil {
		return time.Time{}, time.Time{}, calendarError(err)
	}
	end, err := domain.DateBoundary(last.AddDate(0, 0, 1).Format("2006-01-02"), zone)
	if err != nil {
		return time.Time{}, time.Time{}, calendarError(err)
	}
	return start, end, nil
}
func (a *Analytics) Movements(ctx context.Context, request contract.AnalyticsRequest) (contract.MovementReport, error) {
	if err := ctx.Err(); err != nil {
		return contract.MovementReport{}, err
	}
	r, err := normalizeAnalytics(request, false)
	if err != nil {
		return contract.MovementReport{}, err
	}
	if r.Cursor != "" {
		return a.movementPage(r)
	}
	if err := a.fresh(r); err != nil {
		return contract.MovementReport{}, err
	}
	captured, err := a.journal.CaptureJournal(ctx)
	if err != nil {
		return contract.MovementReport{}, err
	}
	at := captured.At
	from, to, err := a.movementPeriod(r, at)
	if err != nil {
		return contract.MovementReport{}, err
	}
	events, err := a.journal.ReadPeriod(ctx, PeriodQuery{
		From: from, To: to, UpToSequence: captured.Version,
		Types: []domain.EventType{domain.EventDeliveryReceived, domain.EventOrderPlaced, domain.EventBatchExpired, domain.EventBatchWriteoffConfirmed},
	})
	if err != nil {
		return contract.MovementReport{}, inventoryUnavailable(err)
	}
	if err := checkEventSchemas(events); err != nil {
		return contract.MovementReport{}, contract.ErrUnknownEventSchema
	}
	result := contract.MovementReport{Identity: contract.ReportIdentity{Scope: r.Scope, EvaluatedAt: at, StateVersion: captured.Version, PrefixSequence: captured.Version, FilterFingerprint: analyticsFingerprint(r)}, From: from, To: to, Coverage: contract.Coverage{Epoch: r.Scope.RuntimeEpoch, CoverageFrom: a.coverage, Complete: !from.Before(a.coverage), RequestedFrom: from, AvailableFrom: maxTime(from, a.coverage)}, Items: make([]contract.MovementRow, 0)}
	rows := map[string]contract.MovementRow{}
	for _, e := range events {
		if err := ctx.Err(); err != nil {
			return contract.MovementReport{}, err
		}
		add := func(id string, q domain.Quantity, kind int) error {
			p, ok := captured.Products[id]
			if !ok {
				return domain.ErrProductNotFound
			}
			row := rows[id]
			row.ProductID = id
			row.Name = p.Name
			row.BaseUnit = contract.BaseUnit(p.BaseUnit)
			var current contract.Quantity
			switch kind {
			case 0:
				current = row.ReceivedQuantity
			case 1:
				current = row.OrderedQuantity
			case 2:
				current = row.WrittenOffQuantity
			}
			total, err := domain.AddQuantity(domain.Quantity(current), q)
			if err != nil {
				return err
			}
			switch kind {
			case 0:
				row.ReceivedQuantity = contract.Quantity(total)
			case 1:
				row.OrderedQuantity = contract.Quantity(total)
			case 2:
				row.WrittenOffQuantity = contract.Quantity(total)
			}
			rows[id] = row
			return nil
		}
		switch e.Type {
		case domain.EventDeliveryReceived:
			for _, v := range e.Payload.Batches {
				if err = add(v.ProductID, v.ReceivedQuantity, 0); err != nil {
					return contract.MovementReport{}, inventoryUnavailable(err)
				}
			}
		case domain.EventOrderPlaced:
			for _, v := range e.Payload.Order.Allocations {
				if err = add(v.ProductID, v.Quantity, 1); err != nil {
					return contract.MovementReport{}, inventoryUnavailable(err)
				}
			}
		case domain.EventBatchExpired:
			v := e.Payload.Expiry
			if err = add(v.ProductID, v.WrittenOffQuantity, 2); err != nil {
				return contract.MovementReport{}, inventoryUnavailable(err)
			}
		case domain.EventBatchWriteoffConfirmed:
			v := e.Payload.Writeoff
			if err = add(v.ProductID, v.WrittenOffQuantity, 2); err != nil {
				return contract.MovementReport{}, inventoryUnavailable(err)
			}
		}
	}
	for _, row := range rows {
		if analyticsMatch(row.ProductID, row.Name, r) {
			result.Items = append(result.Items, row)
		}
	}
	sort.Slice(result.Items, func(i, j int) bool { return result.Items[i].ProductID < result.Items[j].ProductID })
	return a.captureMovement(r, result)
}
func maxTime(a, b time.Time) time.Time {
	if a.Before(b) {
		return b
	}
	return a
}
func (a *Analytics) purgeLocked() {
	now := a.now()
	for report := range a.reports {
		if !now.Before(report.expires) {
			delete(a.reports, report)
			for _, cursor := range report.pages {
				delete(a.cursors, cursor)
			}
		}
	}
}
func (a *Analytics) cursorLocked(report *capturedReport, index int) (string, error) {
	if cursor := report.pages[index]; cursor != "" {
		return cursor, nil
	}
	cursor := a.newCursor()
	if cursor == "" || len(cursor) > 128 {
		return "", contract.ErrInventoryUnavailable
	}
	if _, exists := a.cursors[cursor]; exists {
		return "", contract.ErrInventoryUnavailable
	}
	report.pages[index] = cursor
	a.cursors[cursor] = reportCursor{report, index}
	return cursor, nil
}
func (a *Analytics) captureStock(r contract.AnalyticsRequest, value contract.StockReport) (contract.StockReport, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.purgeLocked()
	if len(value.Items) <= r.Limit {
		return value, nil
	}
	if len(a.reports) >= 128 {
		return contract.StockReport{Identity: value.Identity, InvalidationRequired: value.InvalidationRequired}, contract.ErrLimitExceeded
	}
	report := &capturedReport{request: r, expires: a.now().Add(5 * time.Minute), stock: &value, pages: map[int]string{}}
	a.reports[report] = true
	page, err := a.stockSliceLocked(report, 0)
	if err != nil {
		delete(a.reports, report)
	}
	return page, err
}
func (a *Analytics) captureMovement(r contract.AnalyticsRequest, value contract.MovementReport) (contract.MovementReport, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.purgeLocked()
	if len(value.Items) <= r.Limit {
		return value, nil
	}
	if len(a.reports) >= 128 {
		return contract.MovementReport{}, contract.ErrLimitExceeded
	}
	report := &capturedReport{request: r, expires: a.now().Add(5 * time.Minute), movement: &value, pages: map[int]string{}}
	a.reports[report] = true
	page, err := a.movementSliceLocked(report, 0)
	if err != nil {
		delete(a.reports, report)
	}
	return page, err
}
func (a *Analytics) lookupLocked(r contract.AnalyticsRequest) (reportCursor, error) {
	a.purgeLocked()
	cursor, ok := a.cursors[r.Cursor]
	if !ok {
		return reportCursor{}, contract.ErrRefreshRequired
	}
	r.Cursor = ""
	if r != cursor.report.request {
		return reportCursor{}, contract.ErrRefreshRequired
	}
	return cursor, nil
}
func (a *Analytics) stockPage(r contract.AnalyticsRequest) (contract.StockReport, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	cursor, err := a.lookupLocked(r)
	if err != nil || cursor.report.stock == nil {
		return contract.StockReport{}, contract.ErrRefreshRequired
	}
	return a.stockSliceLocked(cursor.report, cursor.index)
}
func (a *Analytics) movementPage(r contract.AnalyticsRequest) (contract.MovementReport, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	cursor, err := a.lookupLocked(r)
	if err != nil || cursor.report.movement == nil {
		return contract.MovementReport{}, contract.ErrRefreshRequired
	}
	return a.movementSliceLocked(cursor.report, cursor.index)
}
func (a *Analytics) stockSliceLocked(report *capturedReport, index int) (contract.StockReport, error) {
	value := *report.stock
	end := min(index+report.request.Limit, len(value.Items))
	value.Items = append([]contract.StockRow{}, value.Items[index:end]...)
	value.InvalidationRequired = false
	if end < len(report.stock.Items) {
		cursor, err := a.cursorLocked(report, end)
		if err != nil {
			return contract.StockReport{}, err
		}
		value.NextCursor = cursor
	}
	if index == 0 {
		value.InvalidationRequired = report.stock.InvalidationRequired
	}
	return value, nil
}
func (a *Analytics) movementSliceLocked(report *capturedReport, index int) (contract.MovementReport, error) {
	value := *report.movement
	end := min(index+report.request.Limit, len(value.Items))
	value.Items = append([]contract.MovementRow{}, value.Items[index:end]...)
	if end < len(report.movement.Items) {
		cursor, err := a.cursorLocked(report, end)
		if err != nil {
			return contract.MovementReport{}, err
		}
		value.NextCursor = cursor
	}
	return value, nil
}
