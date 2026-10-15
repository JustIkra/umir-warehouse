package httpapi

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"time"

	"github.com/JustIkra/umir-warehouse/contract"
)

type planningRuntime interface {
	SupplySuggestions(context.Context, contract.PlanningRequest) (contract.SupplySuggestions, error)
	RecommendationInputs(context.Context, contract.PlanningRequest) (contract.RecommendationInputs, error)
}

type planningWindowWire struct {
	From time.Time `json:"from"`
	To   time.Time `json:"to"`
	Days int       `json:"days"`
}

type planningCoverageWire struct {
	Epoch         string    `json:"epoch"`
	CoverageFrom  time.Time `json:"coverageFrom"`
	Complete      bool      `json:"complete"`
	RequestedFrom time.Time `json:"requestedFrom"`
	AvailableFrom time.Time `json:"availableFrom"`
}

type supplyPolicyWire struct {
	Version               string `json:"version"`
	HistoryDays           int    `json:"historyDays"`
	CoverageThresholdDays int    `json:"coverageThresholdDays"`
	TargetDays            int    `json:"targetDays"`
}

type supplyRowWire struct {
	ProductID             string            `json:"productID"`
	ProductName           string            `json:"productName"`
	BaseUnit              contract.BaseUnit `json:"baseUnit"`
	AvailableQuantity     string            `json:"availableQuantity"`
	ConsumedQuantity      string            `json:"consumedQuantity"`
	AverageDailyBaseUnits string            `json:"averageDailyBaseUnits"`
	CoverageDays          *string           `json:"coverageDays"`
	OnePortionDeficit     string            `json:"onePortionDeficit"`
	SuggestedQuantity     string            `json:"suggestedQuantity"`
	BlockedDishIDs        []string          `json:"blockedDishIDs"`
	Reasons               []string          `json:"reasons"`
}

type supplySuggestionsWire struct {
	EvaluatedAt  time.Time            `json:"evaluatedAt"`
	StateVersion string               `json:"stateVersion"`
	Policy       supplyPolicyWire     `json:"policy"`
	Window       planningWindowWire   `json:"window"`
	Coverage     planningCoverageWire `json:"coverage"`
	Items        []supplyRowWire      `json:"items"`
}

type riskBatchWire struct {
	ProductID         string            `json:"productID"`
	ProductName       string            `json:"productName"`
	BatchID           string            `json:"batchID"`
	RemainingQuantity string            `json:"remainingQuantity"`
	BaseUnit          contract.BaseUnit `json:"baseUnit"`
	ExpiresAt         time.Time         `json:"expiresAt"`
	RemainingSeconds  string            `json:"remainingSeconds"`
}

type dishDemandWire struct {
	DishID   string `json:"dishID"`
	Portions string `json:"portions"`
}

type recommendationInputsWire struct {
	EvaluatedAt  time.Time            `json:"evaluatedAt"`
	StateVersion string               `json:"stateVersion"`
	HorizonHours int                  `json:"horizonHours"`
	Window       planningWindowWire   `json:"window"`
	Coverage     planningCoverageWire `json:"coverage"`
	Menu         menuEvaluationWire   `json:"menu"`
	RiskBatches  []riskBatchWire      `json:"riskBatches"`
	Demand       []dishDemandWire     `json:"demand"`
}

func planningWindow(value contract.PlanningWindow) planningWindowWire {
	return planningWindowWire{value.From, value.To, value.Days}
}

func planningCoverage(value contract.Coverage) planningCoverageWire {
	return planningCoverageWire{value.Epoch, value.CoverageFrom, value.Complete, value.RequestedFrom, value.AvailableFrom}
}

func (h *scopedHandlers) planning(w http.ResponseWriter, r *http.Request, runtime Runtime, identity contract.Identity, operation string) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1))
	if err != nil || len(body) != 0 || !noScopedQuery(w, r, false) {
		if err != nil || len(body) != 0 {
			writeScopedError(w, contract.ErrInvalidArgument, false)
		}
		return
	}
	owner, ok := runtime.(planningRuntime)
	if !ok {
		writeScopedError(w, contract.ErrRuntimeNotReady, false)
		return
	}
	request := contract.PlanningRequest{Scope: identity.Scope, Actor: identity.Subject}
	if operation == "privateSupplySuggestions" {
		value, err := owner.SupplySuggestions(r.Context(), request)
		if err != nil {
			writeScopedError(w, err, false)
			return
		}
		policy := value.Policy
		result := supplySuggestionsWire{EvaluatedAt: value.Identity.EvaluatedAt, StateVersion: wireInt(value.Identity.StateVersion), Policy: supplyPolicyWire{policy.Version, policy.HistoryDays, policy.CoverageThresholdDays, policy.TargetDays}, Window: planningWindow(value.Window), Coverage: planningCoverage(value.Coverage), Items: make([]supplyRowWire, 0, len(value.Items))}
		for _, row := range value.Items {
			result.Items = append(result.Items, supplyRowWire{row.ProductID, row.ProductName, row.BaseUnit, wireInt(int64(row.AvailableQuantity)), wireInt(int64(row.ConsumedQuantity)), row.AverageDailyBaseUnits, row.CoverageDays, wireInt(int64(row.OnePortionDeficit)), wireInt(int64(row.SuggestedQuantity)), row.BlockedDishIDs, row.Reasons})
		}
		writePlanningResult(w, value.Identity, result)
		return
	}
	value, err := owner.RecommendationInputs(r.Context(), request)
	if err != nil {
		writeScopedError(w, err, false)
		return
	}
	result := recommendationInputsWire{EvaluatedAt: value.Identity.EvaluatedAt, StateVersion: wireInt(value.Identity.StateVersion), HorizonHours: value.HorizonHours, Window: planningWindow(value.Window), Coverage: planningCoverage(value.Coverage), Menu: wireMenuEvaluation(value.Menu), RiskBatches: make([]riskBatchWire, 0, len(value.RiskBatches)), Demand: make([]dishDemandWire, 0, len(value.Demand))}
	for _, batch := range value.RiskBatches {
		result.RiskBatches = append(result.RiskBatches, riskBatchWire{batch.ProductID, batch.ProductName, batch.BatchID, wireInt(int64(batch.RemainingQuantity)), batch.BaseUnit, batch.ExpiresAt, wireInt(batch.RemainingSeconds)})
	}
	for _, demand := range value.Demand {
		result.Demand = append(result.Demand, dishDemandWire{demand.DishID, wireInt(demand.Portions)})
	}
	writePlanningResult(w, value.Identity, result)
}

func writePlanningResult(w http.ResponseWriter, identity contract.ReportIdentity, value any) {
	data, err := json.Marshal(struct {
		Context businessContextWire `json:"context"`
		Result  any                 `json:"result"`
	}{reportContext(identity), value})
	if err != nil {
		writeScopedError(w, contract.ErrCalculationUnavailable, false)
		return
	}
	if len(data)+1 > 16<<20 {
		writeScopedError(w, contract.ErrLimitExceeded, false)
		return
	}
	writeJSON(w, http.StatusOK, json.RawMessage(data))
}
