//go:build perf

package perf

import (
	"context"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/JustIkra/umir-warehouse/bootstrap"
	"github.com/JustIkra/umir-warehouse/contract"
)

// httpOverheadLimit is how much more than the owner call a commit over HTTP
// may cost at the median on a corpus of 8000 orders.
const httpOverheadLimit = time.Millisecond

func median(samples []time.Duration) time.Duration {
	sorted := slices.Clone(samples)
	slices.Sort(sorted)
	return sorted[len(sorted)/2]
}

// A commit over HTTP costs the owner call plus decoding and encoding, not a
// share of the journal.
func TestHTTPCommitOverhead(t *testing.T) {
	ctx := context.Background()
	msk := time.FixedZone("MSK", 3*3600)
	now := time.Date(2026, 1, 10, 9, 0, 0, 0, msk)
	runtime, err := bootstrap.NewInMemory(bootstrap.Options{Now: func() time.Time { return now }, NewID: newUUID, EventsPerSnapshot: snapshotEvery})
	if err != nil {
		t.Fatal(err)
	}
	if err := runtime.Recover(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.RegisterProduct(ctx, contract.RegisterProductRequest{CommandID: newUUID(), ProductID: "p", Name: "P", BaseUnit: contract.BaseUnitG}); err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.DefineDish(ctx, contract.DefineDishRequest{CommandID: newUUID(), DishID: "d", Name: "D", Ingredients: []contract.Ingredient{{ProductID: "p", Quantity: 1}}}); err != nil {
		t.Fatal(err)
	}
	line := contract.DeliveryLine{ProductID: "p", ManufacturedAt: now.Add(-time.Hour), ShelfLife: contract.ShelfLife{Value: 12, Unit: contract.ShelfLifeMonth}, ReceivedAt: now, Quantity: 1_000_000_000_000}
	if _, err := runtime.ReceiveDelivery(ctx, contract.ReceiveDeliveryRequest{CommandID: newUUID(), DeliveryID: newUUID(), Number: "N", CreatedAt: now, Lines: []contract.DeliveryLine{line}}); err != nil {
		t.Fatal(err)
	}
	positions := []contract.AcceptedPosition{{LineNo: 1, DishID: "d", Portions: 1}}
	for range 8000 {
		if _, err := runtime.CommitAcceptedOrder(ctx, contract.CommitOrderRequest{CommandID: newUUID(), ProposalID: newUUID(), AcceptedPositions: positions}); err != nil {
			t.Fatal(err)
		}
	}
	_, internal, err := bootstrap.NewHTTPHandlers(runtime, bootstrap.HTTPOptions{RequestTimeout: 5 * time.Second, MaxRequestBytes: 1 << 20})
	if err != nil {
		t.Fatal(err)
	}
	const samples = 200
	direct, overHTTP := make([]time.Duration, 0, samples), make([]time.Duration, 0, samples)
	for range samples {
		start := time.Now()
		if _, err := runtime.CommitAcceptedOrder(ctx, contract.CommitOrderRequest{CommandID: newUUID(), ProposalID: newUUID(), AcceptedPositions: positions}); err != nil {
			t.Fatal(err)
		}
		direct = append(direct, time.Since(start))

		body := `{"commandID":"` + newUUID() + `","proposalID":"` + newUUID() + `","acceptedPositions":[{"lineNo":"1","dishID":"d","portions":"1"}]}`
		request := httptest.NewRequest(http.MethodPost, "/internal/v1/order-commits", strings.NewReader(body))
		request.Header.Set("Content-Type", "application/json")
		recorder := httptest.NewRecorder()
		start = time.Now()
		internal.ServeHTTP(recorder, request)
		overHTTP = append(overHTTP, time.Since(start))
		if recorder.Code != http.StatusOK {
			t.Fatalf("commit over HTTP: %d %s", recorder.Code, recorder.Body)
		}
	}
	directMedian, httpMedian := median(direct), median(overHTTP)
	t.Logf("commit at %s orders: owner call %v, over HTTP %v, difference %v (limit %v)", strconv.Itoa(8000+samples), directMedian, httpMedian, httpMedian-directMedian, httpOverheadLimit)
	if httpMedian-directMedian > httpOverheadLimit {
		t.Errorf("a commit over HTTP costs %v more than the owner call; want at most %v", httpMedian-directMedian, httpOverheadLimit)
	}
}
