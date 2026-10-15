package integration_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/JustIkra/umir-warehouse/contract"
)

func TestFINPlanningPrivateReadsAndLeastPrivilege(t *testing.T) {
	f := setup(t)
	runtime, err := f.registry.Resolve(context.Background(), scope(tenantA))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.RegisterProduct(context.Background(), contract.RegisterProductRequest{CommandID: "product", ProductID: "P", Name: "Milk", BaseUnit: contract.BaseUnitML}); err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.DefineDish(context.Background(), contract.DefineDishRequest{CommandID: "dish", DishID: "D", Name: "Latte", Ingredients: []contract.Ingredient{{ProductID: "P", Quantity: 50000}}}); err != nil {
		t.Fatal(err)
	}
	supplyToken := f.token(t, claims(tenantA, "supplySuggestions", "stock.read"), nil)
	consumerClaims := func(operation string, permissions ...string) map[string]any {
		value := claims(tenantA, operation, permissions...)
		value["aud"] = []string{"umir-serving", "umir-warehouse"}
		return value
	}
	recommendationToken := f.token(t, consumerClaims("recommendations", "recommendations.read", "consumer.recommendations.read"), nil)
	supply := f.call("GET", tenantA, "supply-suggestions", "", supplyToken, "gateway-key")
	var captured struct {
		Context struct{ TenantID, RuntimeEpoch, StateVersion string }
		Result  struct {
			StateVersion string
			Coverage     struct{ Complete bool }
			Items        []struct {
				SuggestedQuantity, AverageDailyBaseUnits string
				CoverageDays                             *string
			}
		}
	}
	if supply.Code != 200 || json.Unmarshal(supply.Body.Bytes(), &captured) != nil || captured.Context.TenantID != tenantA || captured.Context.RuntimeEpoch != epoch || captured.Context.StateVersion != "2" || captured.Result.StateVersion != "2" || len(captured.Result.Items) != 1 || captured.Result.Items[0].SuggestedQuantity != "50000" || captured.Result.Items[0].AverageDailyBaseUnits != "0.000000" || captured.Result.Items[0].CoverageDays != nil || captured.Result.Coverage.Complete {
		t.Fatalf("supply wire %d %s", supply.Code, supply.Body)
	}
	inputs := f.call("GET", tenantA, "recommendation-inputs", "", recommendationToken, "serving-key")
	var facts struct {
		Context struct{ StateVersion string }
		Result  struct {
			StateVersion string
			Menu         struct {
				StateVersion string
				Dishes       []struct{ IsAvailable bool }
			}
			RiskBatches, Demand []json.RawMessage
		}
	}
	if inputs.Code != 200 || json.Unmarshal(inputs.Body.Bytes(), &facts) != nil || facts.Context.StateVersion != "2" || facts.Result.StateVersion != "2" || facts.Result.Menu.StateVersion != "2" || len(facts.Result.Menu.Dishes) != 1 || facts.Result.Menu.Dishes[0].IsAvailable || facts.Result.RiskBatches == nil || facts.Result.Demand == nil {
		t.Fatalf("input wire %d %s", inputs.Code, inputs.Body)
	}
	for _, tc := range []struct {
		path, token, key string
		status           int
	}{
		{"supply-suggestions", recommendationToken, "gateway-key", 403},
		{"recommendation-inputs", supplyToken, "serving-key", 403},
		{"recommendation-inputs", recommendationToken, "gateway-key", 403},
		{"recommendation-inputs", f.token(t, consumerClaims("menu", "consumer.menu.evaluate"), nil), "serving-key", 403},
		{"recommendation-inputs", f.token(t, consumerClaims("recommendations", "recommendations.read"), nil), "serving-key", 403},
		{"supply-suggestions?unknown=1", supplyToken, "gateway-key", 400},
		{"recommendation-inputs?unknown=1", recommendationToken, "serving-key", 400},
		{"supply-suggestions", supplyToken, "", 401},
	} {
		w := f.call("GET", tenantA, tc.path, "", tc.token, tc.key)
		if w.Code != tc.status {
			t.Fatalf("guard %s: %d %s", tc.path, w.Code, w.Body)
		}
	}
	if w := f.call("GET", tenantB, "supply-suggestions", "", supplyToken, "gateway-key"); w.Code != 403 {
		t.Fatalf("tenant bypass: %d %s", w.Code, w.Body)
	}
	if w := f.call("GET", tenantA, "supply-suggestions", `{}`, supplyToken, "gateway-key"); w.Code != 400 {
		t.Fatalf("GET body admitted: %d %s", w.Code, w.Body)
	}
}
