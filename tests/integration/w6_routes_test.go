package integration_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/JustIkra/umir-warehouse/bootstrap"
	"github.com/JustIkra/umir-warehouse/contract"
)

func TestW6FrozenRoutesAreInstalled(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	runtime, err := f.registry.Resolve(ctx, scope(tenantA))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = runtime.RegisterProduct(ctx, contract.RegisterProductRequest{CommandID: "fixture-p", ProductID: "P", Name: "P", BaseUnit: contract.BaseUnitG}); err != nil {
		t.Fatal(err)
	}
	if _, err = runtime.DefineDish(ctx, contract.DefineDishRequest{CommandID: "fixture-d", DishID: "D", Name: "D", Ingredients: []contract.Ingredient{{ProductID: "P", Quantity: 1}}}); err != nil {
		t.Fatal(err)
	}
	if _, err = runtime.ReceiveDelivery(ctx, contract.ReceiveDeliveryRequest{CommandID: "fixture-supply", DeliveryID: "supply", Number: "1", CreatedAt: now, Lines: []contract.DeliveryLine{{ProductID: "P", Quantity: 100, ManufacturedAt: now, ReceivedAt: now, ShelfLife: contract.ShelfLife{Value: 1, Unit: contract.ShelfLifeDay}}}}); err != nil {
		t.Fatal(err)
	}
	committed, err := runtime.CommitAcceptedOrder(ctx, contract.CommitOrderRequest{CommandID: "committed-lookup", ProposalID: "known-proposal", AcceptedPositions: []contract.AcceptedPosition{{LineNo: 1, DishID: "D", Portions: 1}}})
	if err != nil || committed.Order == nil {
		t.Fatal("direct public core did not commit", err)
	}
	t.Log("direct public Warehouse core committed an order with real stock before HTTP probes")
	for _, tc := range []struct{ name, method, route, op, cap, key, body string }{
		{"commit", "POST", "order-commits", "confirmOrder", "consumer.order.commit", "serving-key", "{\"commandID\":\"c\",\"proposalID\":\"p\",\"acceptedPositions\":[{\"lineNo\":\"1\",\"dishID\":\"D\",\"portions\":\"1\"}]}"},
		{"order lookup", "GET", "orders?orderID=" + committed.Order.OrderID, "resolveOrder", "consumer.order.resolve", "serving-key", ""},
		{"attempt lookup", "GET", "order-attempts?commandID=committed-lookup", "resolveOrder", "consumer.order.resolve", "serving-key", ""},
		{"history", "GET", "history?fromSequence=0", "history", "stock.read", "gateway-key", ""},
		{"expiring", "GET", "expiring-batches", "expiringBatches", "stock.read", "gateway-key", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cl := claims(tenantA, tc.op, tc.cap)
			if tc.key == "serving-key" {
				cl["aud"] = []string{"umir-serving", "umir-warehouse"}
			}
			w := f.call(tc.method, tenantA, tc.route, tc.body, f.token(t, cl, nil), tc.key)
			t.Logf("observed %d %s", w.Code, w.Body)
			schemas := map[string]string{"commit": "ScopedWOrderCommitResult", "order lookup": "ScopedWOrderLookup", "attempt lookup": "ScopedWOrderAttempt", "history": "ScopedHistory", "expiring": "ScopedExpiringBatches"}
			t.Logf("C2_WIRE %s %s", schemas[tc.name], w.Body.String())
			if strings.Contains(w.Body.String(), "This operation is not installed.") {
				t.Fatal("C2-frozen route passes real JWT trust but has no owner dispatch")
			}
			if w.Code != 200 {
				t.Fatal("healthy scoped read did not succeed")
			}
		})
	}
}

func w6Stock(t *testing.T) (fixture, *bootstrap.Runtime) {
	t.Helper()
	f := setup(t)
	runtime, err := f.registry.Resolve(context.Background(), scope(tenantA))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, err = runtime.RegisterProduct(ctx, contract.RegisterProductRequest{CommandID: "product", ProductID: "P", Name: "P", BaseUnit: contract.BaseUnitG}); err != nil {
		t.Fatal(err)
	}
	if _, err = runtime.DefineDish(ctx, contract.DefineDishRequest{CommandID: "dish", DishID: "D", Name: "D", Ingredients: []contract.Ingredient{{ProductID: "P", Quantity: 1}}}); err != nil {
		t.Fatal(err)
	}
	if _, err = runtime.ReceiveDelivery(ctx, contract.ReceiveDeliveryRequest{CommandID: "supply", DeliveryID: "supply", Number: "1", CreatedAt: now, Lines: []contract.DeliveryLine{{ProductID: "P", Quantity: 100, ManufacturedAt: now, ReceivedAt: now, ShelfLife: contract.ShelfLife{Value: 1, Unit: contract.ShelfLifeDay}}}}); err != nil {
		t.Fatal(err)
	}
	return f, runtime
}
func w6Call(t *testing.T, f fixture, tenant, method, path, op, cap, body string) *httptest.ResponseRecorder {
	t.Helper()
	c := claims(tenant, op, cap)
	key := "gateway-key"
	if strings.HasPrefix(cap, "consumer.") {
		c["aud"] = []string{"umir-serving", "umir-warehouse"}
		key = "serving-key"
	}
	return f.call(method, tenant, path, body, f.token(t, c, nil), key)
}
func TestW6RetainedCommitAndPassiveScope(t *testing.T) {
	f, runtime := w6Stock(t)
	body := `{"commandID":"commit","proposalID":"proposal","acceptedPositions":[{"lineNo":"1","dishID":"D","portions":"1"}]}`
	first := w6Call(t, f, tenantA, "POST", "order-commits", "confirmOrder", "consumer.order.commit", body)
	if first.Code != 200 {
		t.Fatal(first.Code, first.Body)
	}
	var result struct {
		Result struct {
			Order contractOrder `json:"order"`
		} `json:"result"`
	}
	if err := json.Unmarshal(first.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.RegisterProduct(context.Background(), contract.RegisterProductRequest{CommandID: "later", ProductID: "later", Name: "Later", BaseUnit: contract.BaseUnitG}); err != nil {
		t.Fatal(err)
	}
	replay := w6Call(t, f, tenantA, "POST", "order-commits", "confirmOrder", "consumer.order.commit", body)
	if replay.Code != 200 || first.Body.String() != replay.Body.String() {
		t.Fatal("retained result changed", first.Body, replay.Body)
	}
	for _, tc := range []struct{ path, op, cap, want string }{
		{"orders?orderID=" + result.Result.Order.ID, "resolveOrder", "consumer.order.resolve", `"found":true`},
		{"order-attempts?commandID=commit", "resolveOrder", "consumer.order.resolve", `"state":"Completed"`},
		{"order-attempts?commandID=absent", "resolveOrder", "consumer.order.resolve", `"state":"Unseen"`},
	} {
		w := w6Call(t, f, tenantA, "GET", tc.path, tc.op, tc.cap, "")
		if w.Code != 200 || !strings.Contains(w.Body.String(), tc.want) {
			t.Fatal(tc.path, w.Code, w.Body)
		}
		foreign := w6Call(t, f, tenantB, "GET", tc.path, tc.op, tc.cap, "")
		if foreign.Code != 200 || strings.Contains(foreign.Body.String(), result.Result.Order.ID) || strings.Contains(foreign.Body.String(), `"Completed"`) {
			t.Fatal("foreign result", foreign.Code, foreign.Body)
		}
	}
	for _, path := range []string{"orders?proposalID=proposal", "orders?orderID=x&orderID=y", "order-attempts?commandID=commit&proposalID=proposal", "history?fromSequence=-1", "history?fromSequence=01", "expiring-batches?cutoffAt=2026-10-14T09:00:00Z"} {
		op, cap := "resolveOrder", "consumer.order.resolve"
		if strings.HasPrefix(path, "history") {
			op, cap = "history", "stock.read"
		}
		if strings.HasPrefix(path, "expiring") {
			op, cap = "expiringBatches", "stock.read"
		}
		w := w6Call(t, f, tenantA, "GET", path, op, cap, "")
		if w.Code != 400 {
			t.Fatal("nonfrozen input", path, w.Code, w.Body)
		}
	}
	rejected := w6Call(t, f, tenantA, "POST", "order-commits", "confirmOrder", "consumer.order.commit", `{"commandID":"reject","proposalID":"reject-p","acceptedPositions":[{"lineNo":"1","dishID":"missing","portions":"1"}]}`)
	if rejected.Code != 404 {
		t.Fatal(rejected.Code, rejected.Body)
	}
	lookup := w6Call(t, f, tenantA, "GET", "order-attempts?commandID=reject", "resolveOrder", "consumer.order.resolve", "")
	if lookup.Code != 200 || !strings.Contains(lookup.Body.String(), `"state":"Rejected"`) || !strings.Contains(lookup.Body.String(), `"outcome":"not_committed"`) {
		t.Fatal(lookup.Code, lookup.Body)
	}
	t.Logf("C2_WIRE ScopedWOrderAttempt %s", lookup.Body.String())
}

type contractOrder struct {
	ID string `json:"orderID"`
}

func TestW6ExpiryPreparationAndHistoryFacts(t *testing.T) {
	f, runtime := w6Stock(t)
	oldNow := now
	defer func() { now = oldNow }()
	now = now.Add(24 * time.Hour)
	w := w6Call(t, f, tenantA, "GET", "expiring-batches", "expiringBatches", "stock.read", "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"items":[]`) || w.Header().Get("X-Umir-Invalidation-Required") != "true" {
		t.Fatal(w.Code, w.Header(), w.Body)
	}
	t.Logf("C2_WIRE ScopedExpiringBatches %s", w.Body.String())
	history, err := runtime.GetHistory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	event := history.Events[len(history.Events)-1]
	if event.Payload.Pending == nil {
		t.Fatal("due expiry not real", event)
	}
	before := event.Sequence
	lookup := w6Call(t, f, tenantA, "GET", fmt.Sprintf("history?fromSequence=%d", before-1), "history", "stock.read", "")
	if lookup.Code != 200 || !strings.Contains(lookup.Body.String(), `"type":"BatchExpiryPending"`) {
		t.Fatal(lookup.Code, lookup.Body)
	}
	t.Logf("C2_WIRE ScopedHistory %s", lookup.Body.String())
	command := contract.ConfirmWriteoffRequest{CommandID: "final", BatchID: event.Payload.Pending.BatchID, ActorUserID: "11111111-1111-4111-8111-111111111111"}
	if _, err = runtime.ConfirmWriteoff(context.Background(), command); err != nil {
		t.Fatal(err)
	}
	lookup = w6Call(t, f, tenantA, "GET", fmt.Sprintf("history?fromSequence=%d", before), "history", "stock.read", "")
	if lookup.Code != 200 || !strings.Contains(lookup.Body.String(), `"type":"BatchWriteoffConfirmed"`) || strings.Contains(lookup.Body.String(), `"type":"BatchExpiryPending"`) {
		t.Fatal(lookup.Code, lookup.Body)
	}
	t.Logf("C2_WIRE ScopedHistory %s", lookup.Body.String())
}
