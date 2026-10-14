package integration_test

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"strings"
	"testing"
	"time"

	"github.com/JustIkra/umir-warehouse/bootstrap"
	"github.com/JustIkra/umir-warehouse/contract"
)

func TestW3FreshBoundaryPreservesPhysicalStock(t *testing.T) {
	f := setup(t)
	runtime, err := f.registry.RuntimeFor(context.Background(), scope(tenantA))
	if err != nil {
		t.Fatal(err)
	}
	_, err = runtime.RegisterProduct(context.Background(), contract.RegisterProductRequest{CommandID: "w3-product", ProductID: "P", Name: "Product", BaseUnit: contract.BaseUnitG})
	if err != nil {
		t.Fatal(err)
	}
	_, err = runtime.DefineDish(context.Background(), contract.DefineDishRequest{CommandID: "w3-dish", DishID: "D", Name: "Dish", Ingredients: []contract.Ingredient{{ProductID: "P", Quantity: 1}}})
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := runtime.ReceiveDelivery(context.Background(), contract.ReceiveDeliveryRequest{CommandID: "w3-delivery", DeliveryID: "delivery", Number: "1", CreatedAt: now, Lines: []contract.DeliveryLine{{ProductID: "P", Quantity: 100, ManufacturedAt: now.Add(-24 * time.Hour), ReceivedAt: now, ShelfLife: contract.ShelfLife{Value: 1, Unit: contract.ShelfLifeDay}}}})
	if err != nil {
		t.Fatal(err)
	}
	inventory, err := runtime.GetInventory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(inventory.Products) != 1 || inventory.Products[0].Quantity != 100 {
		t.Fatalf("physical quantity lost at expiry: %+v", inventory)
	}
	history, err := runtime.GetHistory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, e := range history.Events {
		if e.Type == "BatchExpiryPending" {
			count++
			if e.SchemaVersion != 2 {
				t.Fatalf("pending schema: %d", e.SchemaVersion)
			}
		}
		if e.Type == "BatchExpired" {
			t.Fatal("new v2 expiry silently wrote off stock")
		}
	}
	if count != 1 {
		t.Fatalf("pending facts=%d batch=%s", count, receipt.BatchIDs[0])
	}
	evaluation, err := runtime.EvaluateOrder(context.Background(), contract.EvaluateOrderRequest{Positions: []contract.Position{{LineNo: 1, DishID: "D", Portions: 1}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(evaluation.AcceptedPositions) != 0 {
		t.Fatal("pending batch allocated")
	}
	version := history.StateVersion
	_, err = runtime.RunExpirations(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	history, err = runtime.GetHistory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if history.StateVersion != version {
		t.Fatal("scheduler duplicated pending transition")
	}
}

func TestW3WriteoffHTTPAndLookup(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	runtime, err := f.registry.RuntimeFor(ctx, scope(tenantA))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = runtime.RegisterProduct(ctx, contract.RegisterProductRequest{CommandID: "p", ProductID: "P", Name: "Product", BaseUnit: contract.BaseUnitG}); err != nil {
		t.Fatal(err)
	}
	receipt, err := runtime.ReceiveDelivery(ctx, contract.ReceiveDeliveryRequest{CommandID: "d", DeliveryID: "d", Number: "1", CreatedAt: now, Lines: []contract.DeliveryLine{{ProductID: "P", Quantity: 100, ManufacturedAt: now.Add(-24 * time.Hour), ReceivedAt: now, ShelfLife: contract.ShelfLife{Value: 1, Unit: contract.ShelfLifeDay}}}})
	if err != nil {
		t.Fatal(err)
	}
	body := `{"commandID":"writeoff-once","batchID":"` + receipt.BatchIDs[0] + `"}`
	token := f.token(t, claims(tenantA, "confirmWriteoff", "writeoff.confirm"), nil)
	w := f.call("POST", tenantA, "writeoffs", body, token, "gateway-key")
	if w.Code != 200 {
		t.Fatalf("confirmation not installed: %d %s", w.Code, w.Body)
	}
	original := w.Body.String()
	w = f.call("POST", tenantA, "writeoffs", body, token, "gateway-key")
	if w.Code != 200 || w.Body.String() != original {
		t.Fatalf("replay changed: %d %s", w.Code, w.Body)
	}
	w = f.call("GET", tenantA, "writeoffs/commands?commandID=writeoff-once&batchID="+receipt.BatchIDs[0], "", f.token(t, claims(tenantA, "resolveWriteoffCommand", "writeoff.confirm"), nil), "gateway-key")
	if w.Code != 200 {
		t.Fatalf("lookup: %d %s", w.Code, w.Body)
	}
	w = f.call("POST", tenantA, "writeoffs", `{"commandID":"other","batchID":"`+receipt.BatchIDs[0]+`"}`, token, "gateway-key")
	if w.Code != 409 {
		t.Fatalf("second command: %d %s", w.Code, w.Body)
	}
}

type w3StockPort interface {
	PrepareStock(context.Context) (contract.StockPreparation, error)
	CalculateStock(context.Context, contract.StockPreparation) (contract.InventoryView, error)
}
type w3FaultRuntime struct {
	bootstrap.ScopedRuntime
	stock w3StockPort
}

func (r w3FaultRuntime) PrepareStock(ctx context.Context) (contract.StockPreparation, error) {
	return r.stock.PrepareStock(ctx)
}
func (r w3FaultRuntime) CalculateStock(context.Context, contract.StockPreparation) (contract.InventoryView, error) {
	return contract.InventoryView{}, contract.ErrInventoryUnavailable
}

type w3FaultDirectory struct{ registry *bootstrap.Registry }

func (d w3FaultDirectory) Epoch() string { return d.registry.Epoch() }
func (d w3FaultDirectory) RuntimeFor(ctx context.Context, s contract.Scope) (bootstrap.ScopedRuntime, error) {
	r, err := d.registry.Resolve(ctx, s)
	if err != nil {
		return nil, err
	}
	return w3FaultRuntime{r, r}, nil
}

func TestW3HTTPFailureRetainsCommittedInvalidation(t *testing.T) {
	f := setup(t)
	clock := now
	registry, err := bootstrap.NewRegistry(bootstrap.Options{Now: func() time.Time { return clock }, NewID: rand.Text, EventsPerSnapshot: 1}, epoch)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := registry.Register(ctx, scope(tenantA)); err != nil {
		t.Fatal(err)
	}
	runtime, err := registry.Resolve(ctx, scope(tenantA))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.RegisterProduct(ctx, contract.RegisterProductRequest{CommandID: "p", ProductID: "P", Name: "Product", BaseUnit: contract.BaseUnitG}); err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.ReceiveDelivery(ctx, contract.ReceiveDeliveryRequest{CommandID: "d", DeliveryID: "d", Number: "1", CreatedAt: now, Lines: []contract.DeliveryLine{{ProductID: "P", Quantity: 100, ManufacturedAt: now, ReceivedAt: now, ShelfLife: contract.ShelfLife{Value: 1, Unit: contract.ShelfLifeDay}}}}); err != nil {
		t.Fatal(err)
	}
	clock = now.Add(24 * time.Hour)
	public, ok := f.private.Public().(ed25519.PublicKey)
	if !ok {
		t.Fatal("unexpected key type")
	}
	verifier, err := bootstrap.NewJWTVerifier(bootstrap.JWTOptions{Now: func() time.Time { return clock }, Keys: keys{public}})
	if err != nil {
		t.Fatal(err)
	}
	f.handler, err = bootstrap.NewScopedHTTPHandler(w3FaultDirectory{registry}, bootstrap.ScopedHTTPOptions{RequestTimeout: time.Second, MaxRequestBytes: 1 << 20, Verifier: verifier, CallerKeys: map[string]string{"umir-gateway": "gateway-key"}})
	if err != nil {
		t.Fatal(err)
	}
	c := claims(tenantA, "inventory", "stock.read")
	c["iat"] = clock.Unix()
	c["nbf"] = clock.Unix()
	c["exp"] = clock.Unix() + 60
	w := f.call("GET", tenantA, "inventory", "", f.token(t, c, nil), "gateway-key")
	if w.Code != 503 {
		t.Fatalf("calculation fault: %d %s", w.Code, w.Body)
	}
	if w.Header().Get("X-Umir-Invalidation-Required") != "true" || w.Header().Get("X-Umir-Committed-Version") != "3" || w.Header().Get("X-Umir-Runtime-Epoch") != epoch {
		t.Fatalf("committed preparation hidden: %+v", w.Header())
	}
	history, err := runtime.GetHistory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(history.Events) != 3 || history.Events[2].Type != "BatchExpiryPending" {
		t.Fatalf("prepared journal=%+v", history)
	}
}

func TestW3FrozenFingerprint(t *testing.T) {
	value, err := contract.WriteoffFingerprint(scope(tenantA), "B1")
	if err != nil {
		t.Fatal(err)
	}
	if value != "ea23235910e79ada27bbae4995e5ec20fdcbb72acf209c54614751175841e3fb" {
		t.Fatal(value)
	}
}

func TestW3HumanOnlyConfirmationAndPrivateResolution(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	runtime, err := f.registry.Resolve(ctx, scope(tenantA))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.RegisterProduct(ctx, contract.RegisterProductRequest{CommandID: "p", ProductID: "P", Name: "Product", BaseUnit: contract.BaseUnitG}); err != nil {
		t.Fatal(err)
	}
	receipt, err := runtime.ReceiveDelivery(ctx, contract.ReceiveDeliveryRequest{CommandID: "d", DeliveryID: "d", Number: "1", CreatedAt: now, Lines: []contract.DeliveryLine{{ProductID: "P", Quantity: 100, ManufacturedAt: now.Add(-24 * time.Hour), ReceivedAt: now, ShelfLife: contract.ShelfLife{Value: 1, Unit: contract.ShelfLifeDay}}}})
	if err != nil {
		t.Fatal(err)
	}
	body := `{"commandID":"once","batchID":"` + receipt.BatchIDs[0] + `"}`
	waiter := claims(tenantA, "confirmWriteoff", "order.confirm", "consumer.order.commit")
	w := f.call("POST", tenantA, "writeoffs", body, f.token(t, waiter, nil), "gateway-key")
	if w.Code != 403 {
		t.Fatalf("waiter authorized: %d", w.Code)
	}
	scheduler := map[string]any{"iss": "urn:umir:auth", "aud": []string{"umir-warehouse"}, "iat": now.Unix(), "nbf": now.Unix(), "exp": now.Unix() + 60, "jti": "55555555-5555-4555-8555-555555555555", "kind": "service", "sub": "service:gateway", "org_id": orgID, "tenant_id": tenantA, "runtime_epoch": epoch, "lifecycle_version": "1", "operation": "expiry.due", "permissions": []string{"expiry.due"}, "azp": "umir-gateway"}
	w = f.call("POST", tenantA, "expiry-runs", `{"runID":"77777777-7777-4777-8777-777777777777"}`, f.token(t, scheduler, nil), "gateway-key")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"finalWriteoffs":0`) || !strings.Contains(w.Body.String(), `"timeZone":"Europe/Moscow"`) {
		t.Fatalf("due=%d %s", w.Code, w.Body)
	}
	w = f.call("POST", tenantA, "writeoffs", body, f.token(t, scheduler, nil), "gateway-key")
	if w.Code != 403 {
		t.Fatal("scheduler confirmed")
	}
	inventory, err := runtime.GetInventory(ctx)
	if err != nil || inventory.Products[0].Quantity != 100 {
		t.Fatal("scheduler wrote off physical quantity", err)
	}
	human := claims(tenantA, "confirmWriteoff", "writeoff.confirm")
	w = f.call("POST", tenantA, "writeoffs", body, f.token(t, human, nil), "gateway-key")
	if w.Code != 200 {
		t.Fatalf("human=%d %s", w.Code, w.Body)
	}
	original := w.Body.String()
	human["sub"] = "11111111-1111-4111-8111-111111111112"
	w = f.call("POST", tenantA, "writeoffs", body, f.token(t, human, nil), "gateway-key")
	if w.Code != 200 || w.Body.String() != original {
		t.Fatal("actor reattributed")
	}
	serving := claims(tenantA, "resolveOrder", "consumer.order.resolve")
	serving["aud"] = []string{"umir-serving", "umir-warehouse"}
	w = f.call("GET", tenantA, "commands?commandID=once", "", f.token(t, serving, nil), "serving-key")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"found":false`) {
		t.Fatalf("consumer leaked writeoff: %d %s", w.Code, w.Body)
	}
	w = f.call("GET", tenantA, "writeoffs/commands?commandID=once&batchID=other", "", f.token(t, claims(tenantA, "resolveWriteoffCommand", "writeoff.confirm"), nil), "gateway-key")
	if w.Code != 409 || strings.Contains(w.Body.String(), "actorUserID") {
		t.Fatalf("retarget lookup=%d %s", w.Code, w.Body)
	}
	w = f.call("GET", tenantB, "writeoffs/commands?commandID=once&batchID="+receipt.BatchIDs[0], "", f.token(t, claims(tenantB, "resolveWriteoffCommand", "writeoff.confirm"), nil), "gateway-key")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"state":"unseen","outcome":"unknown"`) {
		t.Fatalf("neighbor lookup=%d %s", w.Code, w.Body)
	}
}

func TestW3WireSchemas(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	runtime, err := f.registry.Resolve(ctx, scope(tenantA))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.RegisterProduct(ctx, contract.RegisterProductRequest{CommandID: "p", ProductID: "P1", Name: "Synthetic", BaseUnit: contract.BaseUnitG}); err != nil {
		t.Fatal(err)
	}
	receipt, err := runtime.ReceiveDelivery(ctx, contract.ReceiveDeliveryRequest{CommandID: "d", DeliveryID: "d", Number: "1", CreatedAt: now, Lines: []contract.DeliveryLine{{ProductID: "P1", Quantity: 5000, ManufacturedAt: now.Add(-24 * time.Hour), ReceivedAt: now, ShelfLife: contract.ShelfLife{Value: 1, Unit: contract.ShelfLifeDay}}}})
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range []struct{ schema, method, path, op, permission, body string }{{"ScopedInventory", "GET", "inventory", "inventory", "stock.read", ""}, {"PendingBatches", "GET", "pending-writeoffs", "pendingWriteoffs", "stock.read", ""}, {"ScopedWriteoffResult", "POST", "writeoffs", "confirmWriteoff", "writeoff.confirm", `{"commandID":"writeoff-once","batchID":"` + receipt.BatchIDs[0] + `"}`}, {"WriteoffCommandLookup", "GET", "writeoffs/commands?commandID=writeoff-once&batchID=" + receipt.BatchIDs[0], "resolveWriteoffCommand", "writeoff.confirm", ""}} {
		w := f.call(v.method, tenantA, v.path, v.body, f.token(t, claims(tenantA, v.op, v.permission), nil), "gateway-key")
		if w.Code != 200 {
			t.Fatalf("%s=%d %s", v.schema, w.Code, w.Body)
		}
		t.Log("WIRE_SCHEMA", v.schema, w.Body.String())
	}
}

func TestW3WriteoffRejectsUnpairedSurrogates(t *testing.T) {
	f := setup(t)
	token := f.token(t, claims(tenantA, "confirmWriteoff", "writeoff.confirm"), nil)
	for _, body := range []string{`{"commandID":"once","batchID":"\ud800"}`, `{"commandID":"once","batchID":"\udc00"}`, `{"commandID":"once","batchID":"\ud800x"}`} {
		w := f.call("POST", tenantA, "writeoffs", body, token, "gateway-key")
		if w.Code != 400 {
			t.Fatalf("malformed scalar reached owner: %d %s", w.Code, w.Body)
		}
	}
	for _, body := range []string{`{"commandID":"once","batchID":"\ud83d\ude00"}`, `{"commandID":"once","batchID":"\\ud800"}`} {
		w := f.call("POST", tenantA, "writeoffs", body, token, "gateway-key")
		if w.Code != 404 {
			t.Fatalf("valid opaque string rejected: %d %s", w.Code, w.Body)
		}
	}
}
