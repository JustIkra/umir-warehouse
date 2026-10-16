package application

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"slices"
	"sort"
	"strconv"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/JustIkra/umir-warehouse/contract"
)

const productBatchRows = 10000
const productBatchBytes = 16 << 20

type ProductBatchSource interface {
	CaptureProductBatches(context.Context, contract.ProductBatchesRequest) (contract.ProductBatchesPage, error)
}

type PageExpiryTimer interface {
	Stop() bool
}

type batchCapture struct {
	request contract.ProductBatchesRequest
	value   contract.ProductBatchesPage
	expires time.Time
	bytes   int
	pages   map[int]string
	timer   PageExpiryTimer
}

type batchCursor struct {
	capture *batchCapture
	index   int
}

type ProductBatches struct {
	mu        sync.Mutex
	source    ProductBatchSource
	now       func() time.Time
	newCursor func() string
	after     func(time.Duration, func()) PageExpiryTimer
	closed    bool
	bytes     int
	captures  map[*batchCapture]bool
	cursors   map[string]batchCursor
}

func NewProductBatches(source ProductBatchSource, now func() time.Time, newCursor func() string, after func(time.Duration, func()) PageExpiryTimer) (*ProductBatches, error) {
	if source == nil || now == nil || newCursor == nil || after == nil {
		return nil, contract.ErrInvalidArgument
	}
	return &ProductBatches{source: source, now: now, newCursor: newCursor, after: after, captures: map[*batchCapture]bool{}, cursors: map[string]batchCursor{}}, nil
}

func normalizeProductBatches(r contract.ProductBatchesRequest) (contract.ProductBatchesRequest, error) {
	if r.Limit == 0 {
		r.Limit = 100
	}
	if r.Scope.Validate() != nil || r.Actor == "" || r.ProductID == "" || !utf8.ValidString(r.ProductID) || r.Limit < 1 || r.Limit > 500 || len(r.Cursor) > 128 || !utf8.ValidString(r.Cursor) {
		return r, contract.ErrInvalidArgument
	}
	return r, nil
}

func (p *ProductBatches) Read(ctx context.Context, request contract.ProductBatchesRequest) (contract.ProductBatchesPage, error) {
	if err := ctx.Err(); err != nil {
		return contract.ProductBatchesPage{}, err
	}
	r, err := normalizeProductBatches(request)
	if err != nil {
		return contract.ProductBatchesPage{}, err
	}
	if r.Cursor != "" {
		p.mu.Lock()
		defer p.mu.Unlock()
		p.purgeLocked()
		c, ok := p.cursors[r.Cursor]
		r.Cursor = ""
		if p.closed || !ok || r != c.capture.request {
			return contract.ProductBatchesPage{}, contract.ErrRefreshRequired
		}
		return p.pageLocked(c.capture, c.index)
	}
	value, err := p.source.CaptureProductBatches(ctx, r)
	if err != nil {
		return value, err
	}
	size := len(r.Actor) + len(r.ProductID) + 2048
	for _, row := range value.Items {
		b := row.Batch
		size += len(b.BatchID) + len(b.DeliveryID) + len(b.ProductID) + len(b.AppliedTimeZone) + len(b.AppliedSettingsVersion) + len(b.TZDataVersion) + 2048
		if size > productBatchBytes {
			return contract.ProductBatchesPage{Identity: value.Identity, InvalidationRequired: value.InvalidationRequired}, contract.ErrLimitExceeded
		}
	}
	if len(value.Items) > productBatchRows {
		return contract.ProductBatchesPage{Identity: value.Identity, InvalidationRequired: value.InvalidationRequired}, contract.ErrLimitExceeded
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.purgeLocked()
	if p.closed {
		return contract.ProductBatchesPage{}, contract.ErrScopeDeleted
	}
	if len(value.Items) <= r.Limit {
		value.Items = slices.Clone(value.Items)
		return value, nil
	}
	if len(p.captures) >= 128 || size > productBatchBytes-p.bytes {
		return contract.ProductBatchesPage{Identity: value.Identity, InvalidationRequired: value.InvalidationRequired}, contract.ErrLimitExceeded
	}
	c := &batchCapture{request: r, value: value, expires: p.now().Add(5 * time.Minute), bytes: size, pages: map[int]string{}}
	c.value.Items = slices.Clone(value.Items)
	p.captures[c] = true
	p.bytes += size
	page, err := p.pageLocked(c, 0)
	if err != nil {
		p.removeLocked(c)
		return contract.ProductBatchesPage{Identity: value.Identity, InvalidationRequired: value.InvalidationRequired}, err
	}
	c.timer = p.after(5*time.Minute, func() {
		p.mu.Lock()
		defer p.mu.Unlock()
		p.removeLocked(c)
	})
	page.InvalidationRequired = value.InvalidationRequired
	return page, nil
}

func (p *ProductBatches) pageLocked(c *batchCapture, index int) (contract.ProductBatchesPage, error) {
	value := c.value
	end := min(index+c.request.Limit, len(value.Items))
	value.Items = slices.Clone(value.Items[index:end])
	value.InvalidationRequired = false
	if end < len(c.value.Items) {
		cursor := c.pages[end]
		if cursor == "" {
			cursor = p.newCursor()
			if cursor == "" || len(cursor) > 128 || !utf8.ValidString(cursor) {
				return contract.ProductBatchesPage{}, contract.ErrInventoryUnavailable
			}
			if _, exists := p.cursors[cursor]; exists {
				return contract.ProductBatchesPage{}, contract.ErrInventoryUnavailable
			}
			c.pages[end] = cursor
			p.cursors[cursor] = batchCursor{c, end}
		}
		value.NextCursor = cursor
	}
	return value, nil
}

func (p *ProductBatches) removeLocked(c *batchCapture) {
	if !p.captures[c] {
		return
	}
	delete(p.captures, c)
	p.bytes -= c.bytes
	for _, cursor := range c.pages {
		delete(p.cursors, cursor)
	}
	if c.timer != nil {
		c.timer.Stop()
	}
}

func (p *ProductBatches) purgeLocked() {
	for c := range p.captures {
		if !p.now().Before(c.expires) {
			p.removeLocked(c)
		}
	}
}

func (p *ProductBatches) Close() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.closed = true
	for c := range p.captures {
		p.removeLocked(c)
	}
}

func (o *Owner) CaptureProductBatches(ctx context.Context, r contract.ProductBatchesRequest) (contract.ProductBatchesPage, error) {
	if err := o.mu.Lock(ctx); err != nil {
		return contract.ProductBatchesPage{}, inventoryUnavailable(err)
	}
	defer o.mu.Unlock()
	if r.Scope.OrganizationID != o.scope.OrganizationID || r.Scope.RestaurantID != o.scope.RestaurantID || r.Scope.LifecycleVersion != o.scope.LifecycleVersion {
		return contract.ProductBatchesPage{}, contract.ErrForbidden
	}
	if r.Scope.RuntimeEpoch != o.scope.RuntimeEpoch {
		return contract.ProductBatchesPage{}, contract.ErrRuntimeReset
	}
	prepared, transitions, err := o.prepareLocked(ctx)
	fingerprint := sha256.Sum256([]byte(fingerprintJSON("UA1ProductBatches", struct {
		ProductID string
		Limit     string
	}{r.ProductID, strconv.Itoa(r.Limit)})))
	value := contract.ProductBatchesPage{Identity: contract.ReportIdentity{Scope: r.Scope, EvaluatedAt: prepared.EvaluatedAt, StateVersion: prepared.StateVersion, PrefixSequence: prepared.StateVersion, FilterFingerprint: hex.EncodeToString(fingerprint[:])}, ProductID: r.ProductID, Items: []contract.ProductBatchRow{}, InvalidationRequired: len(transitions) > 0}
	if err != nil {
		value.Identity.StateVersion = o.state.StateVersion
		return value, inventoryUnavailable(err)
	}
	product, exists := prepared.State.Products[r.ProductID]
	if !exists {
		return value, contract.ErrProductNotFound
	}
	for _, b := range prepared.State.Batches {
		if b.ProductID == r.ProductID && b.RemainingQuantity > 0 {
			value.Items = append(value.Items, contract.ProductBatchRow{Batch: toContractBatch(b), BaseUnit: contract.BaseUnit(product.BaseUnit)})
		}
	}
	sort.Slice(value.Items, func(i, j int) bool {
		a, b := value.Items[i].Batch, value.Items[j].Batch
		if !a.ExpiresAt.Equal(b.ExpiresAt) {
			return a.ExpiresAt.Before(b.ExpiresAt)
		}
		if !a.ReceivedAt.Equal(b.ReceivedAt) {
			return a.ReceivedAt.Before(b.ReceivedAt)
		}
		return a.BatchID < b.BatchID
	})
	return value, nil
}
