package httpapi

import (
	"context"
	"net/http"
	"strconv"
	"time"

	"github.com/JustIkra/umir-warehouse/contract"
)

type stockRuntime interface {
	PrepareStock(context.Context) (contract.StockPreparation, error)
	CalculateStock(context.Context, contract.StockPreparation) (contract.InventoryView, error)
}
type stockRowWire struct {
	ProductID string            `json:"productID"`
	Name      string            `json:"name"`
	BaseUnit  contract.BaseUnit `json:"baseUnit"`
	Available string            `json:"availableQuantity"`
	Pending   string            `json:"pendingWriteoffQuantity"`
	OnHand    string            `json:"onHandQuantity"`
	Expiring  string            `json:"expiringQuantity"`
}

func (h *scopedHandlers) stock(w http.ResponseWriter, r *http.Request, runtime Runtime, scope contract.Scope, operation string) {
	if !noScopedQuery(w, r, false) {
		return
	}
	owner, ok := runtime.(stockRuntime)
	if !ok {
		writeScopedError(w, contract.ErrRuntimeNotReady, false)
		return
	}
	prepared, err := owner.PrepareStock(r.Context())
	if prepared.InvalidationRequired {
		writeInvalidation(w, scope, prepared.StateVersion)
	}
	if err != nil {
		writeScopedError(w, err, false)
		return
	}
	if operation == "privatePendingWriteoffs" {
		items := make([]batchStatusWire, 0)
		for _, b := range prepared.Batches {
			if b.PendingWriteoff {
				items = append(items, wireBatchStatus(b, scope, h.options.TZDataVersion))
			}
		}
		if len(items) > 500 {
			writeScopedError(w, contract.ErrInventoryUnavailable, false)
			return
		}
		writeJSON(w, 200, struct {
			Context businessContextWire `json:"context"`
			Items   []batchStatusWire   `json:"items"`
		}{businessContextWire{scope.OrganizationID, scope.RestaurantID, scope.RuntimeEpoch, settingsWire{scope.TimeZone, scope.SettingsVersion}, prepared.EvaluatedAt, strconv.FormatInt(prepared.StateVersion, 10)}, items})
		return
	}
	inventory, err := owner.CalculateStock(r.Context(), prepared)
	if err != nil {
		writeScopedError(w, err, false)
		return
	}
	if len(inventory.Products) > 500 {
		writeScopedError(w, contract.ErrInventoryUnavailable, false)
		return
	}
	rows := make([]stockRowWire, 0, len(inventory.Products))
	for _, p := range inventory.Products {
		rows = append(rows, stockRowWire{p.ProductID, p.Name, p.BaseUnit, strconv.FormatInt(int64(p.AvailableQuantity), 10), strconv.FormatInt(int64(p.PendingWriteoffQuantity), 10), strconv.FormatInt(int64(p.Quantity), 10), strconv.FormatInt(int64(p.ExpiringQuantity), 10)})
	}

	writeScopedResult(w, scope, prepared.EvaluatedAt, prepared.StateVersion, struct {
		Items []stockRowWire `json:"items"`
	}{rows})
}

type batchStatusWire struct {
	Batch    batchV2Wire  `json:"batch"`
	Status   string       `json:"status"`
	Settings settingsWire `json:"appliedSettings"`
}
type batchV2Wire struct {
	BatchID           string        `json:"batchID"`
	DeliveryID        string        `json:"deliveryID"`
	LineNo            string        `json:"lineNo"`
	ProductID         string        `json:"productID"`
	ManufacturedAt    time.Time     `json:"manufacturedAt"`
	ShelfLife         shelfLifeWire `json:"shelfLife"`
	ReceivedAt        time.Time     `json:"receivedAt"`
	ExpiresAt         time.Time     `json:"expiresAt"`
	ReceivedQuantity  string        `json:"receivedQuantity"`
	RemainingQuantity string        `json:"remainingQuantity"`
	Settings          settingsWire  `json:"appliedSettings"`
	TZDataVersion     string        `json:"tzdataVersion"`
}

func wireBatchStatus(b contract.Batch, scope contract.Scope, tzdata string) batchStatusWire {
	if tzdata == "" {
		tzdata = "unknown"
	}
	settings := settingsWire{b.AppliedTimeZone, b.AppliedSettingsVersion}
	if b.TZDataVersion != "" {
		tzdata = b.TZDataVersion
	}
	return batchStatusWire{batchV2Wire{b.BatchID, b.DeliveryID, strconv.FormatInt(b.LineNo, 10), b.ProductID, b.ManufacturedAt, shelfLifeWire{strconv.FormatInt(b.ShelfLife.Value, 10), string(b.ShelfLife.Unit)}, b.ReceivedAt, b.ExpiresAt, strconv.FormatInt(int64(b.ReceivedQuantity), 10), strconv.FormatInt(int64(b.RemainingQuantity), 10), settings, tzdata}, "pending_writeoff", settings}
}
