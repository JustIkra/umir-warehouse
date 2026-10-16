package httpapi

import (
	"context"
	"net/http"
	"strings"

	"github.com/JustIkra/umir-warehouse/contract"
)

type commandAttemptReader interface {
	FindOrderAttemptByCommand(context.Context, string) (contract.OrderAttempt, error)
}

func (h *scopedHandlers) scopedOrder(w http.ResponseWriter, r *http.Request, runtime Runtime, scope contract.Scope, operation string) {
	ctx := r.Context()
	if operation == "consumerCommit" {
		if !noScopedQuery(w, r, true) {
			return
		}
		var input orderCommitRequest
		if decodeRequest(r, h.options.MaxRequestBytes, &input) != nil {
			writeScopedError(w, contract.ErrInvalidArgument, true)
			return
		}
		command, e1 := requiredString(input.CommandID)
		proposal, e2 := requiredString(input.ProposalID)
		positions, e3 := parsePositions(input.AcceptedPositions)
		if e1 != nil || e2 != nil || e3 != nil || strings.HasPrefix(command, "expiration/") {
			writeScopedError(w, contract.ErrInvalidArgument, true)
			return
		}
		accepted := make([]contract.AcceptedPosition, len(positions))
		for i, p := range positions {
			accepted[i] = contract.AcceptedPosition(p)
		}
		value, err := runtime.CommitAcceptedOrder(ctx, contract.CommitOrderRequest{CommandID: command, ProposalID: proposal, AcceptedPositions: accepted})
		if err != nil {
			writeScopedError(w, err, true)
			return
		}
		if value.Kind == contract.CommitResultPending {
			writeScopedError(w, contract.ErrUnknownWriteOutcome, true)
			return
		}
		if !validCommitPayload(value) {
			writeScopedError(w, contract.ErrUnknownWriteOutcome, true)
			return
		}
		if value.Kind == contract.CommitResultRecheckRequired {
			writeScopedResult(w, scope, value.Evaluation.EvaluatedAt, value.Evaluation.StateVersion, wireCommit(value))
			return
		}
		// The answer carries where the order was placed, whichever command placed it.
		placement, found, err := runtime.FindOrderPlacement(ctx, value.Order.OrderID)
		if err != nil || !found {
			writeScopedError(w, contract.ErrUnknownWriteOutcome, true)
			return
		}
		writeScopedResult(w, scope, placement.RecordedAt, placement.Sequence, wireCommit(value))
		return
	}
	query := "orderID"
	if operation == "consumerAttempt" {
		query = "commandID"
	}
	values, err := queryValues(r, query)
	if err != nil {
		writeScopedError(w, contract.ErrInvalidArgument, false)
		return
	}
	id, err := requiredQuery(values, query)
	if err != nil {
		writeScopedError(w, contract.ErrInvalidArgument, false)
		return
	}
	if operation == "consumerAttempt" {
		reader, ok := runtime.(commandAttemptReader)
		if !ok {
			writeScopedError(w, contract.ErrRuntimeNotReady, false)
			return
		}
		value, err := reader.FindOrderAttemptByCommand(ctx, id)
		if err != nil {
			writeScopedError(w, err, false)
			return
		}
		if !validAttemptPayload(value) {
			writeScopedError(w, contract.ErrInventoryUnavailable, false)
			return
		}
		result := orderAttemptWire{State: value.State}
		if value.Result != nil {
			wire := wireCommit(*value.Result)
			result.Result = &wire
		}
		if value.Error != nil {
			result.Error = &errorWire{Code: value.Error.Code, Message: value.Error.Message, Outcome: string(value.Error.Outcome)}
		}
		head, err := runtime.Head(ctx)
		if err != nil {
			writeScopedError(w, err, false)
			return
		}
		writeScopedResult(w, scope, head.EvaluatedAt, head.StateVersion, result)
		return
	}
	head, err := runtime.Head(ctx)
	if err != nil {
		writeScopedError(w, err, false)
		return
	}
	order, found, err := runtime.FindOrder(ctx, id)
	if err != nil {
		writeScopedError(w, err, false)
		return
	}
	result := foundOrderWire{}
	if found {
		wire := wireOrder(order)
		result.Found = true
		result.Order = &wire
	}
	writeScopedResult(w, scope, head.EvaluatedAt, head.StateVersion, result)
}
