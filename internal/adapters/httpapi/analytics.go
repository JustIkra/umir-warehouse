package httpapi

import (
	"context"
	"net/http"
	"strconv"
	"time"

	"github.com/JustIkra/umir-warehouse/contract"
)

type analyticsRuntime interface {
	AnalyticsStock(context.Context, contract.AnalyticsRequest) (contract.StockReport, error)
	AnalyticsMovements(context.Context, contract.AnalyticsRequest) (contract.MovementReport, error)
}
type pageIdentityWire struct {
	TenantID          string       `json:"tenantID"`
	Epoch             string       `json:"epoch"`
	StateVersion      string       `json:"stateVersion"`
	PrefixSequence    string       `json:"prefixSequence"`
	FilterFingerprint string       `json:"filterFingerprint"`
	Settings          settingsWire `json:"settings"`
}

func reportContext(v contract.ReportIdentity) businessContextWire {
	s := v.Scope
	return businessContextWire{s.OrganizationID, s.RestaurantID, s.RuntimeEpoch, settingsWire{s.TimeZone, s.SettingsVersion}, v.EvaluatedAt, strconv.FormatInt(v.StateVersion, 10)}
}
func reportPage(v contract.ReportIdentity) pageIdentityWire {
	s := v.Scope
	return pageIdentityWire{s.RestaurantID, s.RuntimeEpoch, strconv.FormatInt(v.StateVersion, 10), strconv.FormatInt(v.PrefixSequence, 10), v.FilterFingerprint, settingsWire{s.TimeZone, s.SettingsVersion}}
}
func (h *scopedHandlers) analytics(w http.ResponseWriter, r *http.Request, runtime Runtime, identity contract.Identity, operation string) {
	allowed := []string{"expectedSettingsVersion", "productID", "search", "limit", "cursor"}
	stock := operation == "privateAnalyticsstock"
	if stock {
		allowed = append(allowed, "riskHours")
	} else {
		allowed = append(allowed, "fromDate", "throughDate")
	}
	values, err := queryValues(r, allowed...)
	expected, e := requiredQuery(values, "expectedSettingsVersion")
	if err != nil || e != nil || !contract.ValidVersion(expected) {
		writeScopedError(w, contract.ErrInvalidArgument, false)
		return
	}
	request := contract.AnalyticsRequest{Scope: identity.Scope, Actor: identity.Subject, ExpectedSettingsVersion: expected, ProductID: values.Get("productID"), Search: values.Get("search"), Cursor: values.Get("cursor"), FromDate: values.Get("fromDate"), ThroughDate: values.Get("throughDate")}
	for _, key := range []string{"productID", "cursor", "fromDate", "throughDate"} {
		if _, exists := values[key]; exists && values.Get(key) == "" {
			writeScopedError(w, contract.ErrInvalidArgument, false)
			return
		}
	}
	for _, key := range []string{"limit", "riskHours"} {
		if _, ok := values[key]; ok {
			value, err := strconv.Atoi(values.Get(key))
			if err != nil || value < 1 || strconv.Itoa(value) != values.Get(key) {
				writeScopedError(w, contract.ErrInvalidArgument, false)
				return
			}
			if key == "limit" {
				request.Limit = value
			} else {
				request.RiskHours = value
			}
		}
	}
	owner, ok := runtime.(analyticsRuntime)
	if !ok {
		writeScopedError(w, contract.ErrRuntimeNotReady, false)
		return
	}
	if stock {
		value, err := owner.AnalyticsStock(r.Context(), request)
		if value.InvalidationRequired {
			writeInvalidation(w, value.Identity.Scope, value.Identity.StateVersion)
		}
		if err != nil {
			writeScopedError(w, err, false)
			return
		}
		items := make([]stockRowWire, 0, len(value.Items))
		for _, row := range value.Items {
			items = append(items, stockRowWire{row.ProductID, row.Name, row.BaseUnit, strconv.FormatInt(int64(row.AvailableQuantity), 10), strconv.FormatInt(int64(row.PendingWriteoffQuantity), 10), strconv.FormatInt(int64(row.OnHandQuantity), 10), strconv.FormatInt(int64(row.ExpiringQuantity), 10)})
		}
		writeJSON(w, 200, struct {
			Context    businessContextWire `json:"context"`
			Page       pageIdentityWire    `json:"page"`
			RiskHours  int                 `json:"riskHours"`
			RiskCutoff time.Time           `json:"riskCutoff"`
			Items      []stockRowWire      `json:"items"`
			NextCursor string              `json:"nextCursor,omitempty"`
		}{reportContext(value.Identity), reportPage(value.Identity), value.RiskHours, value.RiskCutoff, items, value.NextCursor})
	} else {
		value, err := owner.AnalyticsMovements(r.Context(), request)
		if err != nil {
			writeScopedError(w, err, false)
			return
		}
		type rowWire struct {
			ProductID  string            `json:"productID"`
			Name       string            `json:"name"`
			BaseUnit   contract.BaseUnit `json:"baseUnit"`
			Received   string            `json:"receivedQuantity"`
			Ordered    string            `json:"orderedQuantity"`
			WrittenOff string            `json:"writtenOffQuantity"`
		}
		items := make([]rowWire, 0, len(value.Items))
		for _, row := range value.Items {
			items = append(items, rowWire{row.ProductID, row.Name, row.BaseUnit, strconv.FormatInt(int64(row.ReceivedQuantity), 10), strconv.FormatInt(int64(row.OrderedQuantity), 10), strconv.FormatInt(int64(row.WrittenOffQuantity), 10)})
		}
		type coverageWire struct {
			Epoch         string    `json:"epoch"`
			CoverageFrom  time.Time `json:"coverageFrom"`
			Complete      bool      `json:"complete"`
			RequestedFrom time.Time `json:"requestedFrom"`
			AvailableFrom time.Time `json:"availableFrom"`
		}
		c := value.Coverage
		writeJSON(w, 200, struct {
			Context    businessContextWire `json:"context"`
			Page       pageIdentityWire    `json:"page"`
			From       time.Time           `json:"from"`
			To         time.Time           `json:"to"`
			Coverage   coverageWire        `json:"coverage"`
			Items      []rowWire           `json:"items"`
			NextCursor string              `json:"nextCursor,omitempty"`
		}{reportContext(value.Identity), reportPage(value.Identity), value.From, value.To, coverageWire{c.Epoch, c.CoverageFrom, c.Complete, c.RequestedFrom, c.AvailableFrom}, items, value.NextCursor})
	}
}
