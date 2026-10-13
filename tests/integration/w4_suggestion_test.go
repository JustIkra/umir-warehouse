package integration_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/JustIkra/umir-warehouse/bootstrap"
	"github.com/JustIkra/umir-warehouse/contract"
)

type w4SuggestionWire struct {
	Context struct {
		OrganizationID string `json:"orgID"`
		TenantID       string `json:"tenantID"`
		Epoch          string `json:"runtimeEpoch"`
		Settings       struct {
			TimeZone string `json:"timeZone"`
			Version  string `json:"settingsVersion"`
		} `json:"settings"`
		EvaluatedAt time.Time `json:"evaluatedAt"`
		Version     string    `json:"stateVersion"`
	} `json:"context"`
	Result struct {
		Kind     string `json:"kind"`
		ID       string `json:"suggestedID"`
		Reserved bool   `json:"reserved"`
	} `json:"result"`
}

func w4Suggest(t *testing.T, f fixture, tenant, kind, want string) w4SuggestionWire {
	t.Helper()
	w := f.call("GET", tenant, "id-suggestions?kind="+kind, "", f.token(t, claims(tenant, "suggestID", "catalog.write"), nil), "gateway-key")
	if w.Code != 200 {
		t.Fatalf("suggest %s: %d %s", kind, w.Code, w.Body)
	}
	var got w4SuggestionWire
	decoder := json.NewDecoder(w.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&got); err != nil {
		t.Fatal(err)
	}
	if got.Result.Kind != kind || got.Result.ID != want || got.Result.Reserved || got.Context.TenantID != tenant || got.Context.OrganizationID != orgID || got.Context.Epoch != epoch || !got.Context.EvaluatedAt.Equal(now) || got.Context.Settings.TimeZone != "Europe/Moscow" || got.Context.Settings.Version != "1" {
		t.Fatalf("suggestion: %+v", got)
	}
	encoded, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("WIRE_SCHEMA ScopedSuggestion %s", encoded)
	return got
}

func w4Runtime(t *testing.T, f fixture, tenant string) *bootstrap.Runtime {
	t.Helper()
	r, err := f.registry.Resolve(context.Background(), scope(tenant))
	if err != nil {
		t.Fatal(err)
	}
	return r
}
func w4Product(t *testing.T, r *bootstrap.Runtime, id string) {
	t.Helper()
	_, err := r.RegisterProduct(context.Background(), contract.RegisterProductRequest{CommandID: "product-" + id, ProductID: id, Name: "product", BaseUnit: contract.BaseUnitG})
	if err != nil {
		t.Fatal(err)
	}
}
func w4Dish(t *testing.T, r *bootstrap.Runtime, id string) {
	t.Helper()
	_, err := r.DefineDish(context.Background(), contract.DefineDishRequest{CommandID: "dish-" + id, DishID: id, Name: "dish", Ingredients: []contract.Ingredient{{ProductID: "P", Quantity: 1}}})
	if err != nil {
		t.Fatal(err)
	}
}

func TestW4LowestFreeIndependentCatalogsAndTenants(t *testing.T) {
	f := setup(t)
	a := w4Runtime(t, f, tenantA)
	b := w4Runtime(t, f, tenantB)
	w4Suggest(t, f, tenantA, "product", "1")
	w4Suggest(t, f, tenantA, "dish", "1")
	w4Product(t, a, "P")
	for _, id := range []string{"01", "1.0", "+1", "0", "-1", "١", "手動", "9223372036854775808", "18446744073709551616"} {
		w4Product(t, a, id)
		w4Dish(t, a, id)
	}
	w4Suggest(t, f, tenantA, "product", "1")
	w4Suggest(t, f, tenantA, "dish", "1")
	w4Product(t, a, "1")
	w4Product(t, a, "3")
	w4Suggest(t, f, tenantA, "product", "2")
	w4Suggest(t, f, tenantA, "dish", "1")
	w4Dish(t, a, "1")
	w4Dish(t, a, "3")
	w4Suggest(t, f, tenantA, "dish", "2")
	w4Product(t, b, "P")
	w4Product(t, b, "2")
	w4Dish(t, b, "2")
	w4Suggest(t, f, tenantB, "product", "1")
	w4Suggest(t, f, tenantB, "dish", "1")
	w4Product(t, a, "2")
	w4Dish(t, a, "2")
	w4Suggest(t, f, tenantA, "product", "4")
	w4Suggest(t, f, tenantA, "dish", "4")
}

func TestW4SuggestionIsPassiveAndDoesNotReserve(t *testing.T) {
	f := setup(t)
	r := w4Runtime(t, f, tenantA)
	w4Product(t, r, "P")
	before, err := r.GetHistory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for range 3 {
		got := w4Suggest(t, f, tenantA, "product", "1")
		if got.Context.Version != "1" {
			t.Fatal(got)
		}
		w4Suggest(t, f, tenantA, "dish", "1")
	}
	after, err := r.GetHistory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, after) {
		t.Fatal("suggestion changed events or version")
	}
	w4Product(t, r, "7")
	w4Dish(t, r, "7")
	w4Suggest(t, f, tenantA, "product", "1")
	w4Suggest(t, f, tenantA, "dish", "1")
	w4Product(t, r, "1")
	w4Dish(t, r, "1")
	w4Suggest(t, f, tenantA, "product", "2")
	w4Suggest(t, f, tenantA, "dish", "2")
}

func TestW4SuggestionDoesNotChangePendingStock(t *testing.T) {
	f := setup(t)
	r := w4Runtime(t, f, tenantA)
	w4Product(t, r, "P")
	_, err := r.ReceiveDelivery(context.Background(), contract.ReceiveDeliveryRequest{CommandID: "delivery", DeliveryID: "delivery", Number: "delivery", CreatedAt: now, Lines: []contract.DeliveryLine{{ProductID: "P", ManufacturedAt: now.Add(-48 * time.Hour), ReceivedAt: now.Add(-24 * time.Hour), ShelfLife: contract.ShelfLife{Value: 1, Unit: contract.ShelfLifeDay}, Quantity: 1}}})
	if err != nil {
		t.Fatal(err)
	}
	before, err := r.GetHistory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	w4Suggest(t, f, tenantA, "product", "1")
	w4Suggest(t, f, tenantA, "dish", "1")
	after, err := r.GetHistory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, after) {
		t.Fatal("suggestion processed due expiry")
	}
}

func TestW4ConcurrentCreateConflictAndRetainedRetry(t *testing.T) {
	for _, kind := range []string{"product", "dish"} {
		t.Run(kind, func(t *testing.T) {
			f := setup(t)
			r := w4Runtime(t, f, tenantA)
			w4Product(t, r, "P")
			w4Suggest(t, f, tenantA, kind, "1")
			path, op, conflict := "products", "registerProduct", "ProductConflict"
			if kind == "dish" {
				path, op, conflict = "dishes", "defineDish", "DishConflict"
			}
			bodies := [2]string{}
			for i := range bodies {
				if kind == "product" {
					bodies[i] = fmt.Sprintf(`{"commandID":"create-%d","productID":"1","name":"name-%d","baseUnit":"g"}`, i, i)
				} else {
					bodies[i] = fmt.Sprintf(`{"commandID":"create-%d","dishID":"1","name":"name-%d","ingredients":[{"productID":"P","quantity":"1"}]}`, i, i)
				}
			}
			token := f.token(t, claims(tenantA, op, "catalog.write"), nil)
			responses := [2]*httptest.ResponseRecorder{}
			start := make(chan struct{})
			var wg sync.WaitGroup
			for i := range responses {
				wg.Go(func() { <-start; responses[i] = f.call("POST", tenantA, path, bodies[i], token, "gateway-key") })
			}
			close(start)
			wg.Wait()
			winner := -1
			for i, w := range responses {
				switch w.Code {
				case 200:
					if winner != -1 {
						t.Fatal("both creates succeeded")
					}
					winner = i
				case 409:
					var e struct {
						Code    string `json:"code"`
						Outcome string `json:"outcome"`
					}
					if err := json.Unmarshal(w.Body.Bytes(), &e); err != nil {
						t.Fatal(err)
					}
					if e.Code != conflict || e.Outcome != "not_committed" {
						t.Fatal(w.Body)
					}
				default:
					t.Fatalf("create: %d %s", w.Code, w.Body)
				}
			}
			if winner == -1 {
				t.Fatal("no create succeeded")
			}
			original := responses[winner].Body.String()
			w4Suggest(t, f, tenantA, kind, "2")
			replay := f.call("POST", tenantA, path, bodies[winner], token, "gateway-key")
			if replay.Code != 200 || replay.Body.String() != original {
				t.Fatalf("retry changed: %d %s vs %s", replay.Code, replay.Body, original)
			}
			history, err := r.GetHistory(context.Background())
			if err != nil || history.StateVersion != 2 || len(history.Events) != 2 {
				t.Fatalf("history %+v %v", history, err)
			}
		})
	}
}

func TestW4SuggestionRejectsInvalidQueriesAndTrust(t *testing.T) {
	f := setup(t)
	token := f.token(t, claims(tenantA, "suggestID", "catalog.write"), nil)
	for _, query := range []string{"", "?kind=", "?kind=Product", "?kind=batch", "?kind=product&kind=dish", "?kind=product&extra=x", "?kind=%zz"} {
		w := f.call("GET", tenantA, "id-suggestions"+query, "", token, "gateway-key")
		if w.Code != 400 || strings.Contains(w.Body.String(), "suggestedID") {
			t.Fatalf("query %q: %d %s", query, w.Code, w.Body)
		}
	}
	for _, c := range []map[string]any{claims(tenantA, "suggestID", "menu.read"), claims(tenantB, "suggestID", "catalog.write"), claims(tenantA, "products", "catalog.write")} {
		w := f.call("GET", tenantA, "id-suggestions?kind=product", "", f.token(t, c, nil), "gateway-key")
		if w.Code != 403 {
			t.Fatalf("trust: %d %s", w.Code, w.Body)
		}
	}
}
