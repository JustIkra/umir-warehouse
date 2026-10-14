package integration_test

import (
	"context"
	"encoding/json"
	"net/url"
	"testing"
	"time"

	"github.com/JustIkra/umir-warehouse/contract"
)

func TestUA1ProductBatchesOpaquePath(t *testing.T) {
	f := setup(t)
	r, err := f.registry.Resolve(context.Background(), scope(tenantA))
	if err != nil {
		t.Fatal(err)
	}
	product := "товар%2F"
	if _, err := r.RegisterProduct(context.Background(), contract.RegisterProductRequest{CommandID: "opaque-product", ProductID: product, Name: "Milk", BaseUnit: contract.BaseUnitML}); err != nil {
		t.Fatal(err)
	}
	token := f.token(t, claims(tenantA, "productBatches", "stock.read"), nil)
	w := f.call("GET", tenantA, "products/"+url.PathEscape(product)+"/batches", "", token, "gateway-key")
	if refused := f.call("GET", tenantA, "products/group%2Fproduct/batches", "", token, "gateway-key"); refused.Code != 400 {
		t.Fatalf("encoded slash admitted: %d", refused.Code)
	}
	var page struct{ Result struct{ ProductID string } }
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &page) != nil || page.Result.ProductID != product {
		t.Fatalf("opaque nested identity lost: %d %s", w.Code, w.Body)
	}
}

func TestUA1ProductBatchesPrivateHTTP(t *testing.T) {
	f := setup(t)
	r, err := f.registry.Resolve(context.Background(), scope(tenantA))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.RegisterProduct(context.Background(), contract.RegisterProductRequest{CommandID: "ua1-product", ProductID: "P", Name: "Milk", BaseUnit: contract.BaseUnitML}); err != nil {
		t.Fatal(err)
	}
	for i, manufacture := range []time.Time{now.Add(-24 * time.Hour), now} {
		id := []string{"due", "available"}[i]
		if _, err := r.ReceiveDelivery(context.Background(), contract.ReceiveDeliveryRequest{CommandID: id, DeliveryID: id, Number: "1", CreatedAt: now, Lines: []contract.DeliveryLine{{ProductID: "P", Quantity: 1500, ManufacturedAt: manufacture, ReceivedAt: now, ShelfLife: contract.ShelfLife{Value: 1, Unit: contract.ShelfLifeDay}}}}); err != nil {
			t.Fatal(err)
		}
	}
	token := f.token(t, claims(tenantA, "productBatches", "stock.read"), nil)
	w := f.call("GET", tenantA, "products/P/batches?limit=1", "", token, "gateway-key")
	if w.Code != 200 {
		t.Fatalf("batch route not composed: %d %s", w.Code, w.Body)
	}
	var page struct {
		Context struct{ OrgID, TenantID, RuntimeEpoch, StateVersion string }
		Result  struct {
			ProductID, NextCursor string
			Page                  struct{ Epoch, StateVersion, PrefixSequence string }
			Items                 []struct {
				Status, BaseUnit string
				Batch            struct {
					ProductID, RemainingQuantity, TZDataVersion string
					AppliedSettings                             struct{ TimeZone, SettingsVersion string }
				}
			}
		}
	}
	if err := json.Unmarshal(w.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if page.Context.OrgID != orgID || page.Context.TenantID != tenantA || page.Context.RuntimeEpoch != epoch || page.Result.ProductID != "P" || page.Result.Page.StateVersion != page.Context.StateVersion || page.Result.Page.PrefixSequence != page.Context.StateVersion || page.Result.Page.Epoch != epoch || len(page.Result.Items) != 1 || page.Result.Items[0].Status != "pending_writeoff" || page.Result.Items[0].BaseUnit != "ml" || page.Result.Items[0].Batch.RemainingQuantity != "1500" || page.Result.Items[0].Batch.AppliedSettings.TimeZone != "Europe/Moscow" || page.Result.NextCursor == "" {
		t.Fatalf("invalid scoped FEFO projection: %+v", page)
	}
	w = f.call("GET", tenantA, "products/P/batches?limit=1&cursor="+page.Result.NextCursor, "", token, "gateway-key")
	page.Result.NextCursor = ""
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &page) != nil || page.Result.Items[0].Status != "available" || page.Result.NextCursor != "" {
		t.Fatalf("continuation: %d %s", w.Code, w.Body)
	}
	w = f.call("GET", tenantA, "products/missing/batches", "", token, "gateway-key")
	if w.Code != 404 {
		t.Fatalf("missing product = %d %s", w.Code, w.Body)
	}
}

func TestUA1ProductBatchesPrivateGuards(t *testing.T) {
	f := setup(t)
	token := f.token(t, claims(tenantA, "productBatches", "stock.read"), nil)
	for _, query := range []string{"?limit=0", "?limit=501", "?limit=01", "?limit=1&limit=2", "?cursor=", "?cursor=x&cursor=y", "?foreign=1"} {
		w := f.call("GET", tenantA, "products/P/batches"+query, "", token, "gateway-key")
		if w.Code != 400 {
			t.Fatalf("query %q: %d %s", query, w.Code, w.Body)
		}
	}
	if w := f.call("GET", tenantA, "products/P/batches", `{}`, token, "gateway-key"); w.Code != 400 {
		t.Fatalf("GET body accepted: %d", w.Code)
	}
	if w := f.call("GET", tenantA, "products/P/batches", "", token, ""); w.Code != 401 {
		t.Fatalf("transport key bypass: %d", w.Code)
	}
	if w := f.call("GET", tenantA, "products/P/batches", "", f.token(t, claims(tenantA, "productBatches", "order.read"), nil), "gateway-key"); w.Code != 403 {
		t.Fatalf("capability bypass: %d", w.Code)
	}
	if w := f.call("GET", tenantB, "products/P/batches", "", token, "gateway-key"); w.Code != 403 {
		t.Fatalf("tenant bypass: %d", w.Code)
	}
}
