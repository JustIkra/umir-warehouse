package contract

import "time"

type PlanningRequest struct {
	Scope Scope
	Actor string
}

type PlanningWindow struct {
	From, To time.Time
	Days     int
}

type SupplyPolicy struct {
	Version                                        string
	HistoryDays, CoverageThresholdDays, TargetDays int
}

type SupplyRow struct {
	ProductID, ProductName                                                    string
	BaseUnit                                                                  BaseUnit
	AvailableQuantity, ConsumedQuantity, OnePortionDeficit, SuggestedQuantity Quantity
	AverageDailyBaseUnits                                                     string
	CoverageDays                                                              *string
	BlockedDishIDs, Reasons                                                   []string
}

type SupplySuggestions struct {
	Identity ReportIdentity
	Policy   SupplyPolicy
	Window   PlanningWindow
	Coverage Coverage
	Items    []SupplyRow
}

type RiskBatch struct {
	ProductID, ProductName, BatchID string
	RemainingQuantity               Quantity
	BaseUnit                        BaseUnit
	ExpiresAt                       time.Time
	RemainingSeconds                int64
}

type DishDemand struct {
	DishID   string
	Portions int64
}

type RecommendationInputs struct {
	Identity     ReportIdentity
	HorizonHours int
	Window       PlanningWindow
	Coverage     Coverage
	Menu         MenuEvaluation
	RiskBatches  []RiskBatch
	Demand       []DishDemand
}
