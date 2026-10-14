package httpapi

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/JustIkra/umir-warehouse/contract"
)

type productBatchesRuntime interface {
	ProductBatches(context.Context, contract.ProductBatchesRequest) (contract.ProductBatchesPage, error)
}

func (h *scopedHandlers) productBatches(w http.ResponseWriter, r *http.Request, runtime Runtime, identity contract.Identity) {
	body, bodyErr := io.ReadAll(http.MaxBytesReader(w, r.Body, 1))
	parts := strings.Split(r.URL.EscapedPath(), "/")
	encoded := parts[len(parts)-2]
	productID, pathErr := url.PathUnescape(encoded)
	values, err := queryValues(r, "limit", "cursor")
	if bodyErr != nil || len(body) != 0 || err != nil || pathErr != nil || encoded != url.PathEscape(productID) || productID == "." || productID == ".." || !utf8.ValidString(productID) || strings.ContainsAny(productID, "/\x00\r\n") {
		writeScopedError(w, contract.ErrInvalidArgument, false)
		return
	}
	request := contract.ProductBatchesRequest{Scope: identity.Scope, Actor: identity.Subject, ProductID: productID, Cursor: values.Get("cursor")}
	if _, exists := values["cursor"]; exists && request.Cursor == "" {
		writeScopedError(w, contract.ErrInvalidArgument, false)
		return
	}
	if _, exists := values["limit"]; exists {
		request.Limit, err = strconv.Atoi(values.Get("limit"))
		if err != nil || request.Limit < 1 || request.Limit > 500 || strconv.Itoa(request.Limit) != values.Get("limit") {
			writeScopedError(w, contract.ErrInvalidArgument, false)
			return
		}
	}
	owner, ok := runtime.(productBatchesRuntime)
	if !ok {
		writeScopedError(w, contract.ErrRuntimeNotReady, false)
		return
	}
	value, err := owner.ProductBatches(r.Context(), request)
	if value.InvalidationRequired {
		writeInvalidation(w, value.Identity.Scope, value.Identity.StateVersion)
	}
	if err != nil {
		writeScopedError(w, err, false)
		return
	}
	type rowWire struct {
		Batch    batchV2Wire       `json:"batch"`
		BaseUnit contract.BaseUnit `json:"baseUnit"`
		Status   string            `json:"status"`
	}
	items := make([]rowWire, 0, len(value.Items))
	for _, row := range value.Items {
		status := "available"
		if row.Batch.PendingWriteoff {
			status = "pending_writeoff"
		}
		items = append(items, rowWire{wireBatchStatus(row.Batch, value.Identity.Scope, h.options.TZDataVersion).Batch, row.BaseUnit, status})
	}
	writeJSON(w, 200, struct {
		Context businessContextWire `json:"context"`
		Result  struct {
			ProductID   string           `json:"productID"`
			EvaluatedAt time.Time        `json:"evaluatedAt"`
			Page        pageIdentityWire `json:"page"`
			Items       []rowWire        `json:"items"`
			NextCursor  string           `json:"nextCursor,omitempty"`
		} `json:"result"`
	}{reportContext(value.Identity), struct {
		ProductID   string           `json:"productID"`
		EvaluatedAt time.Time        `json:"evaluatedAt"`
		Page        pageIdentityWire `json:"page"`
		Items       []rowWire        `json:"items"`
		NextCursor  string           `json:"nextCursor,omitempty"`
	}{value.ProductID, value.Identity.EvaluatedAt, reportPage(value.Identity), items, value.NextCursor}})
}
