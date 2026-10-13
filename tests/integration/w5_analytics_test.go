package integration_test

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"testing"
	"time"

	"github.com/JustIkra/umir-warehouse/bootstrap"
	"github.com/JustIkra/umir-warehouse/contract"
	"github.com/JustIkra/umir-warehouse/tests/fixtures/protocol"
)

func TestW5StockReportConservationAndCutoff(t *testing.T) {
	f := setup(t)
	r, err := f.registry.Resolve(context.Background(), scope(tenantA))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = r.RegisterProduct(context.Background(), contract.RegisterProductRequest{CommandID: "w5-p", ProductID: "P", Name: "Milk", BaseUnit: contract.BaseUnitML}); err != nil {
		t.Fatal(err)
	}
	for i, life := range []int64{1, 2, 4} {
		id := []string{"pending", "risk", "safe"}[i]
		at := now
		if i == 0 {
			at = now.Add(-24 * time.Hour)
		}
		if _, err = r.ReceiveDelivery(context.Background(), contract.ReceiveDeliveryRequest{CommandID: id, DeliveryID: id, Number: "1", CreatedAt: now, Lines: []contract.DeliveryLine{{ProductID: "P", Quantity: 100, ManufacturedAt: at, ReceivedAt: now, ShelfLife: contract.ShelfLife{Value: life, Unit: contract.ShelfLifeDay}}}}); err != nil {
			t.Fatal(err)
		}
	}
	w := f.call("GET", tenantA, "analytics/stock?expectedSettingsVersion=1", "", f.token(t, claims(tenantA, "analyticsstock", "analytics.read"), nil), "gateway-key")
	if w.Code != 200 {
		t.Fatalf("stock report unavailable: %d %s", w.Code, w.Body)
	}
	var report struct {
		RiskHours  int
		RiskCutoff time.Time
		Items      []struct{ AvailableQuantity, PendingWriteoffQuantity, OnHandQuantity, ExpiringQuantity string }
		Context    struct {
			StateVersion string
			EvaluatedAt  time.Time
		}
		Page struct{ PrefixSequence string }
	}
	if err = json.Unmarshal(w.Body.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	if len(report.Items) != 1 {
		t.Fatalf("rows=%d", len(report.Items))
	}
	row := report.Items[0]
	if row.AvailableQuantity != "200" || row.PendingWriteoffQuantity != "100" || row.OnHandQuantity != "300" || row.ExpiringQuantity != "100" || report.RiskHours != 48 || !report.RiskCutoff.Equal(now.Add(48*time.Hour)) || report.Context.StateVersion != report.Page.PrefixSequence {
		t.Fatalf("conservation/cutoff: %+v", report)
	}
}

func TestW5MovementCoverageAndRecordedTime(t *testing.T) {
	f := setup(t)
	r, err := f.registry.Resolve(context.Background(), scope(tenantA))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = r.RegisterProduct(context.Background(), contract.RegisterProductRequest{CommandID: "w5-p", ProductID: "P", Name: "Milk", BaseUnit: contract.BaseUnitML}); err != nil {
		t.Fatal(err)
	}
	if _, err = r.ReceiveDelivery(context.Background(), contract.ReceiveDeliveryRequest{CommandID: "w5-d", DeliveryID: "d", Number: "1", CreatedAt: now.Add(-48 * time.Hour), Lines: []contract.DeliveryLine{{ProductID: "P", Quantity: 100, ManufacturedAt: now.Add(-48 * time.Hour), ReceivedAt: now, ShelfLife: contract.ShelfLife{Value: 4, Unit: contract.ShelfLifeDay}}}}); err != nil {
		t.Fatal(err)
	}
	w := f.call("GET", tenantA, "analytics/movements?expectedSettingsVersion=1", "", f.token(t, claims(tenantA, "analyticsmovements", "analytics.read"), nil), "gateway-key")
	if w.Code != 200 {
		t.Fatalf("movement report unavailable: %d %s", w.Code, w.Body)
	}
	var report struct {
		Coverage struct {
			Complete     bool
			CoverageFrom time.Time
		}
		Items    []struct{ ReceivedQuantity string }
		From, To time.Time
	}
	if err = json.Unmarshal(w.Body.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	if report.Coverage.Complete || !report.Coverage.CoverageFrom.Equal(now) || len(report.Items) != 1 || report.Items[0].ReceivedQuantity != "100" || !report.From.Before(now) || !report.To.After(now) {
		t.Fatalf("coverage/RecordedAt: %+v", report)
	}
}

func TestW5DeliveryAcceptsInstantAndReportsCapturedSettings(t *testing.T) {
	f := setup(t)
	r, err := f.registry.Resolve(context.Background(), scope(tenantA))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = r.RegisterProduct(context.Background(), contract.RegisterProductRequest{CommandID: "w5-p", ProductID: "P", Name: "Milk", BaseUnit: contract.BaseUnitML}); err != nil {
		t.Fatal(err)
	}
	body := `{"commandID":"w5-http","deliveryID":"d","number":"1","expectedSettingsVersion":"1","createdAt":{"instant":"` + now.UTC().Format(time.RFC3339) + `"},"lines":[{"productID":"P","manufacturedAt":{"instant":"` + now.UTC().Format(time.RFC3339) + `"},"receivedAt":{"instant":"` + now.UTC().Format(time.RFC3339) + `"},"shelfLife":{"value":"1","unit":"month"},"quantity":"100"}]}`
	w := f.call("POST", tenantA, "deliveries", body, f.token(t, claims(tenantA, "receiveDelivery", "delivery.create"), nil), "gateway-key")
	if w.Code != 200 {
		t.Fatalf("v2 time input rejected: %d %s", w.Code, w.Body)
	}
	var report struct {
		Result struct {
			Batches []struct {
				AppliedSettings struct{ TimeZone, SettingsVersion string }
				TZDataVersion   string
			}
		}
	}
	if err = json.Unmarshal(w.Body.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	if len(report.Result.Batches) != 1 || report.Result.Batches[0].AppliedSettings.TimeZone != "Europe/Moscow" || report.Result.Batches[0].AppliedSettings.SettingsVersion != "1" || report.Result.Batches[0].TZDataVersion == "" {
		t.Fatalf("captured settings: %+v", report)
	}
}

func TestW5HTTPStrictAnalyticsQueriesAndCapturedZoneRetry(t *testing.T) {
	f := setup(t)
	for _, query := range []string{"", "?expectedSettingsVersion=1&limit=0", "?expectedSettingsVersion=1&limit=501", "?expectedSettingsVersion=1&limit=01", "?expectedSettingsVersion=1&riskHours=8785", "?expectedSettingsVersion=1&riskHours=1&riskHours=2", "?expectedSettingsVersion=1&foreign=1", "?expectedSettingsVersion=1&cursor="} {
		w := f.call("GET", tenantA, "analytics/stock"+query, "", f.token(t, claims(tenantA, "analyticsstock", "analytics.read"), nil), "gateway-key")
		if w.Code != 400 {
			t.Fatalf("query %s = %d %s", query, w.Code, w.Body)
		}
	}
	for _, query := range []string{"?expectedSettingsVersion=1&fromDate=2026-01-01", "?expectedSettingsVersion=1&fromDate=2026-04-31&throughDate=2026-05-01", "?expectedSettingsVersion=1&fromDate=2025-01-01&throughDate=2026-01-02"} {
		w := f.call("GET", tenantA, "analytics/movements"+query, "", f.token(t, claims(tenantA, "analyticsmovements", "analytics.read"), nil), "gateway-key")
		if w.Code != 400 {
			t.Fatalf("movement query %s = %d %s", query, w.Code, w.Body)
		}
	}
	w := f.call("GET", tenantA, "analytics/stock?expectedSettingsVersion=2", "", f.token(t, claims(tenantA, "analyticsstock", "analytics.read"), nil), "gateway-key")
	if w.Code != 409 {
		t.Fatalf("stale fresh=%d %s", w.Code, w.Body)
	}
	r, err := f.registry.Resolve(context.Background(), scope(tenantA))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = r.RegisterProduct(context.Background(), contract.RegisterProductRequest{CommandID: "zone-p", ProductID: "P", Name: "Product", BaseUnit: contract.BaseUnitG}); err != nil {
		t.Fatal(err)
	}
	body := `{"commandID":"zone","deliveryID":"zone","number":"1","expectedSettingsVersion":"1","createdAt":{"instant":"` + now.Format(time.RFC3339) + `"},"lines":[{"productID":"P","manufacturedAt":{"civil":{"localDateTime":"2026-01-29T02:30:00"}},"receivedAt":{"instant":"` + now.Format(time.RFC3339) + `"},"shelfLife":{"value":"2","unit":"month"},"quantity":"10"}]}`
	c := claims(tenantA, "receiveDelivery", "delivery.create")
	c["time_zone"] = "Europe/Berlin"
	first := f.call("POST", tenantA, "deliveries", body, f.token(t, c, nil), "gateway-key")
	if first.Code != 200 {
		t.Fatalf("civil intake=%d %s", first.Code, first.Body)
	}
	c["time_zone"] = "Asia/Tokyo"
	c["settings_version"] = "2"
	retry := f.call("POST", tenantA, "deliveries", body, f.token(t, c, nil), "gateway-key")
	if retry.Code != 200 || retry.Body.String() != first.Body.String() {
		t.Fatalf("HTTP retained original context changed=%d %s", retry.Code, retry.Body)
	}
	t.Logf("WIRE_SCHEMA ScopedDeliveryReceipt %s", first.Body.String())
	w = f.call("GET", tenantA, "analytics/stock?expectedSettingsVersion=2", "", f.token(t, func() map[string]any {
		v := claims(tenantA, "analyticsstock", "analytics.read")
		v["time_zone"] = "Asia/Tokyo"
		v["settings_version"] = "2"
		return v
	}(), nil), "gateway-key")
	if w.Code != 200 {
		t.Fatalf("new settings analytics=%d %s", w.Code, w.Body)
	}
	t.Logf("WIRE_SCHEMA StockReport %s", w.Body.String())
}

type w5FaultRuntime struct {
	*bootstrap.Runtime
	reader bootstrap.AnalyticsStockReader
}

func (r w5FaultRuntime) AnalyticsStock(ctx context.Context, v contract.AnalyticsRequest) (contract.StockReport, error) {
	return r.AnalyticsStockUsing(ctx, v, r.reader)
}

type w5FaultDirectory struct {
	registry *bootstrap.Registry
	reader   bootstrap.AnalyticsStockReader
}

func (d w5FaultDirectory) Epoch() string { return d.registry.Epoch() }
func (d w5FaultDirectory) RuntimeFor(ctx context.Context, s contract.Scope) (bootstrap.ScopedRuntime, error) {
	r, err := d.registry.Resolve(ctx, s)
	if err != nil {
		return nil, err
	}
	return w5FaultRuntime{r, d.reader}, nil
}

type w5FaultReader struct {
	r     *bootstrap.Runtime
	fault *protocol.AnalyticsFault
}

func (r w5FaultReader) PrepareStock(ctx context.Context) (contract.StockPreparation, error) {
	return r.r.PrepareStock(ctx)
}
func (r w5FaultReader) CalculateStock(ctx context.Context, v contract.StockPreparation) (contract.InventoryView, error) {
	return r.fault.Calculate(ctx, "analyticsstock", "read-once", v, r.r.CalculateStock)
}

func TestW5PostPrepareAnalyticsFaultRetainsRealCommittedInvalidation(t *testing.T) {
	f := setup(t)
	clock := now
	registry, err := bootstrap.NewRegistry(bootstrap.Options{Now: func() time.Time { return clock }, NewID: rand.Text, EventsPerSnapshot: 1}, epoch)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err = registry.Register(ctx, scope(tenantA)); err != nil {
		t.Fatal(err)
	}
	r, err := registry.Resolve(ctx, scope(tenantA))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = r.RegisterProduct(ctx, contract.RegisterProductRequest{CommandID: "p", ProductID: "P", Name: "P", BaseUnit: contract.BaseUnitG}); err != nil {
		t.Fatal(err)
	}
	if _, err = r.ReceiveDelivery(ctx, contract.ReceiveDeliveryRequest{CommandID: "d", DeliveryID: "d", Number: "1", CreatedAt: clock, Lines: []contract.DeliveryLine{{ProductID: "P", Quantity: 17, ManufacturedAt: clock, ReceivedAt: clock, ShelfLife: contract.ShelfLife{Value: 1, Unit: contract.ShelfLifeDay}}}}); err != nil {
		t.Fatal(err)
	}
	fault, err := protocol.NewAnalyticsFault(time.Now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = fault.Arm(protocol.FaultArm{FaultID: "99999999-9999-4999-8999-999999999999", Point: protocol.AnalyticsPoint, OperationID: "analyticsstock", CommandID: "read-once", ExpireAfterMs: 30000, Count: 1}); err != nil {
		t.Fatal(err)
	}
	public, ok := f.private.Public().(ed25519.PublicKey)
	if !ok {
		t.Fatal("invalid test public key")
	}
	verifier, err := bootstrap.NewJWTVerifier(bootstrap.JWTOptions{Now: func() time.Time { return clock }, Keys: keys{public}})
	if err != nil {
		t.Fatal(err)
	}
	handler, err := bootstrap.NewScopedHTTPHandler(w5FaultDirectory{registry, w5FaultReader{r, fault}}, bootstrap.ScopedHTTPOptions{RequestTimeout: time.Second, MaxRequestBytes: 1 << 20, Verifier: verifier, CallerKeys: map[string]string{"umir-gateway": "gateway-key"}})
	if err != nil {
		t.Fatal(err)
	}
	f.handler = handler
	clock = clock.Add(24 * time.Hour)
	c := claims(tenantA, "analyticsstock", "analytics.read")
	c["iat"] = clock.Unix()
	c["nbf"] = clock.Unix()
	c["exp"] = clock.Unix() + 60
	w := f.call("GET", tenantA, "analytics/stock?expectedSettingsVersion=1", "", f.token(t, c, nil), "gateway-key")
	if w.Code != 503 || w.Header().Get("X-Umir-Committed-Version") != "3" || w.Header().Get("X-Umir-Invalidation-Required") != "true" || w.Header().Get("X-Umir-Runtime-Epoch") != epoch {
		t.Fatalf("real preparation lost %d %+v %s", w.Code, w.Header(), w.Body)
	}
	t.Logf("WIRE_SCHEMA Error %s", w.Body.String())
	state, err := fault.Read("99999999-9999-4999-8999-999999999999")
	if err != nil || state.State != "hit" || state.Hits != 1 || state.OwnerOutcome != "committed" {
		t.Fatalf("fault barrier=%+v %v", state, err)
	}
	c["operation"] = "analyticsmovements"
	w = f.call("GET", tenantA, "analytics/movements?expectedSettingsVersion=1", "", f.token(t, c, nil), "gateway-key")
	if w.Code != 200 {
		t.Fatalf("journal lost=%d %s", w.Code, w.Body)
	}
	t.Logf("WIRE_SCHEMA MovementReport %s", w.Body.String())
	c["operation"] = "analyticsstock"
	w = f.call("GET", tenantA, "analytics/stock?expectedSettingsVersion=1", "", f.token(t, c, nil), "gateway-key")
	if w.Code != 200 || w.Header().Get("X-Umir-Committed-Version") != "" {
		t.Fatalf("fault replay or duplicate transition=%d %s", w.Code, w.Body)
	}
	t.Logf("WIRE_SCHEMA StockReport %s", w.Body.String())
}
