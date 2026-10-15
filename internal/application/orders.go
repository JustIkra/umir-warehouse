package application

import (
	"context"
	"errors"
	"sort"

	"github.com/JustIkra/umir-warehouse/contract"
	"github.com/JustIkra/umir-warehouse/internal/domain"
)

func (o *Owner) EvaluateOrder(ctx context.Context, request contract.EvaluateOrderRequest) (contract.OrderEvaluation, error) {
	positions, err := toDomainPositions(request.Positions)
	if err != nil {
		return contract.OrderEvaluation{}, err
	}
	prepared, err := o.Prepare(ctx)
	if err != nil {
		return contract.OrderEvaluation{}, err
	}
	evaluation, err := domain.PureEvaluatePositions(prepared.State, positions)
	if err != nil {
		return contract.OrderEvaluation{}, publicEvaluationError(err)
	}
	evaluation.EvaluatedAt = prepared.EvaluatedAt
	return toContractOrderEvaluation(evaluation), nil
}

func (o *Owner) EvaluateMenu(ctx context.Context) (contract.MenuEvaluation, error) {
	prepared, err := o.Prepare(ctx)
	if err != nil {
		return contract.MenuEvaluation{}, err
	}
	return evaluateMenu(ctx, prepared)
}

func evaluateMenu(ctx context.Context, prepared ReadState) (contract.MenuEvaluation, error) {
	dishIDs := make([]string, 0, len(prepared.State.Dishes))
	for dishID := range prepared.State.Dishes {
		dishIDs = append(dishIDs, dishID)
	}
	sort.Strings(dishIDs)
	evaluation := contract.MenuEvaluation{EvaluatedAt: prepared.EvaluatedAt, StateVersion: prepared.StateVersion, Dishes: make([]contract.DishEvaluation, 0, len(dishIDs))}
	for _, dishID := range dishIDs {
		if err := ctx.Err(); err != nil {
			return contract.MenuEvaluation{}, err
		}
		dish := prepared.State.Dishes[dishID]
		positions := []domain.Position{{LineNo: 1, DishID: dishID, Portions: 1}}
		result, allocations, err := domain.EvaluatePositionsWithAllocations(prepared.State, positions)
		if err != nil {
			return contract.MenuEvaluation{}, publicEvaluationError(err)
		}
		item := contract.DishEvaluation{DishID: dish.DishID, DishName: dish.Name, IsAvailable: len(result.AcceptedPositions) == 1, Deficits: make([]contract.Deficit, 0), Allocations: make([]contract.MenuAllocation, 0)}
		if len(result.Changes) > 0 {
			item.Deficits = toContractDeficits(result.Changes[0].Deficits)
		}
		if item.IsAvailable {
			for _, allocation := range allocations {
				batch := prepared.State.Batches[allocation.BatchID]
				product := prepared.State.Products[allocation.ProductID]
				item.Allocations = append(item.Allocations, contract.MenuAllocation{
					ProductID:   allocation.ProductID,
					ProductName: product.Name,
					BatchID:     allocation.BatchID,
					Quantity:    contract.Quantity(allocation.Quantity),
					BaseUnit:    contract.BaseUnit(product.BaseUnit),
					ExpiresAt:   batch.ExpiresAt,
				})
			}
		}
		evaluation.Dishes = append(evaluation.Dishes, item)
	}
	return evaluation, nil
}

func (o *Owner) CommitAcceptedOrder(ctx context.Context, request contract.CommitOrderRequest) (contract.CommitResult, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if request.CommandID == "" || request.ProposalID == "" || isReservedCommandID(request.CommandID) {
		return contract.CommitResult{}, contract.ErrInvalidArgument
	}
	fingerprint := canonicalConfirmOrderFP(request.ProposalID)
	for {
		var pendingOrder *domain.Order
		var pendingErr error
		if o.pending != nil {
			pendingRecord, found, err := o.resolvePendingLocked(ctx)
			pendingErr = err
			if found {
				if pendingRecord.CommandID == request.CommandID {
					return recordedCommitOrder(pendingRecord, fingerprint)
				}
				if pendingRecord.Result.Order != nil && pendingRecord.Result.Order.ProposalID == request.ProposalID {
					order := domain.CloneOrder(*pendingRecord.Result.Order)
					pendingOrder = &order
				}
			}
		}
		result, found, err := o.findRecordedCommitOrder(ctx, request.CommandID, fingerprint)
		if err != nil {
			return contract.CommitResult{}, err
		}
		if found {
			return result, nil
		}
		if pendingOrder != nil {
			order := toContractOrder(*pendingOrder)
			return contract.CommitResult{Kind: contract.CommitResultOrderCommitted, Order: &order}, nil
		}
		existing, found, err := o.events.FindOrderByProposal(ctx, request.ProposalID)
		if err != nil {
			return contract.CommitResult{}, err
		}
		if found {
			order := toContractOrder(existing)
			return contract.CommitResult{Kind: contract.CommitResultOrderCommitted, Order: &order}, nil
		}
		if pendingErr != nil && !errors.Is(pendingErr, domain.ErrConcurrentModification) {
			return contract.CommitResult{}, inventoryUnavailable(pendingErr)
		}
		requested, err := toDomainAcceptedPositions(request.AcceptedPositions)
		if err != nil {
			return contract.CommitResult{}, err
		}
		if len(requested) == 0 {
			return contract.CommitResult{}, contract.ErrInvalidArgument
		}
		prepared, _, err := o.prepareLocked(ctx)
		if err != nil {
			recorded, found, lookupErr := o.findRecordedCommitOrder(ctx, request.CommandID, fingerprint)
			if lookupErr != nil {
				return contract.CommitResult{}, lookupErr
			}
			if found {
				return recorded, nil
			}
			order, found, lookupErr := o.events.FindOrderByProposal(ctx, request.ProposalID)
			if lookupErr == nil && found {
				publicOrder := toContractOrder(order)
				return contract.CommitResult{Kind: contract.CommitResultOrderCommitted, Order: &publicOrder}, nil
			}
			if lookupErr != nil {
				return contract.CommitResult{}, lookupErr
			}
			return contract.CommitResult{}, inventoryUnavailable(err)
		}
		if recorded, exists := prepared.State.Commands[request.CommandID]; exists {
			return recordedCommitOrder(recorded, fingerprint)
		}
		if orderID, exists := prepared.State.OrdersByProposal[request.ProposalID]; exists {
			order, found := prepared.State.Orders[orderID]
			if !found {
				return contract.CommitResult{}, domain.ErrInvalidRecordedBatch
			}
			publicOrder := toContractOrder(order)
			return contract.CommitResult{Kind: contract.CommitResultOrderCommitted, Order: &publicOrder}, nil
		}
		evaluation, allocations, err := domain.EvaluatePositionsWithAllocations(prepared.State, acceptedAsPositions(requested))
		if err != nil {
			return contract.CommitResult{}, publicEvaluationError(err)
		}
		evaluation.EvaluatedAt = prepared.EvaluatedAt
		if !sameAcceptedPositions(requested, evaluation.AcceptedPositions) {
			publicEvaluation := toContractOrderEvaluation(evaluation)
			return contract.CommitResult{Kind: contract.CommitResultRecheckRequired, Evaluation: &publicEvaluation}, nil
		}
		orderID := o.newID()
		eventID := o.newID()
		if orderID == "" || eventID == "" || orderID == eventID {
			return contract.CommitResult{}, contract.ErrInvalidArgument
		}
		accepted := append([]domain.AcceptedPosition(nil), requested...)
		order := domain.Order{OrderID: orderID, ProposalID: request.ProposalID, AcceptedPositions: accepted, Allocations: allocations, PlacedAt: prepared.EvaluatedAt}
		event := domain.Event{EventID: eventID, RecordedAt: prepared.EvaluatedAt, Type: domain.EventOrderPlaced, Payload: domain.EventPayload{Order: &order}}
		command := domain.CommandBatch{ExpectedVersion: prepared.State.StateVersion, CommandID: request.CommandID, CommandType: "ConfirmOrder", CommandFingerprint: fingerprint, Events: []domain.Event{event}}
		if _, err := domain.ApplyRecordedBatch(prepared.State, asRecordedBatch(command)); err != nil {
			if errors.Is(err, domain.ErrDishNotFound) {
				return contract.CommitResult{}, contract.ErrDishNotFound
			}
			return contract.CommitResult{}, err
		}
		record, found, err := o.appendOwnerCommandLocked(ctx, command)
		if errors.Is(err, domain.ErrConcurrentModification) {
			continue
		}
		if err != nil {
			return contract.CommitResult{}, err
		}
		if !found {
			return contract.CommitResult{}, domain.ErrUnknownWriteOutcome
		}
		return recordedCommitOrder(record, fingerprint)
	}
}

func (o *Owner) findRecordedCommitOrder(ctx context.Context, commandID, fingerprint string) (contract.CommitResult, bool, error) {
	record, found, err := o.findCommand(ctx, commandID, "ConfirmOrder", fingerprint)
	if err != nil || !found {
		return contract.CommitResult{}, found, err
	}
	result, err := recordedCommitOrder(record, fingerprint)
	return result, true, err
}

func canonicalConfirmOrderFP(proposalID string) string {
	return fingerprintJSON("ConfirmOrder", struct {
		ProposalID string `json:"proposalID"`
	}{ProposalID: proposalID})
}

func toDomainPositions(positions []contract.Position) ([]domain.Position, error) {
	result := make([]domain.Position, len(positions))
	seen := make(map[int64]struct{}, len(positions))
	for index, position := range positions {
		if position.LineNo <= 0 || position.DishID == "" || position.Portions <= 0 {
			return nil, contract.ErrInvalidArgument
		}
		if _, exists := seen[position.LineNo]; exists {
			return nil, contract.ErrInvalidArgument
		}
		seen[position.LineNo] = struct{}{}
		result[index] = domain.Position{LineNo: position.LineNo, DishID: position.DishID, Portions: position.Portions}
	}
	return result, nil
}

func toDomainAcceptedPositions(positions []contract.AcceptedPosition) ([]domain.AcceptedPosition, error) {
	result := make([]domain.AcceptedPosition, len(positions))
	seen := make(map[int64]struct{}, len(positions))
	for index, position := range positions {
		if position.LineNo <= 0 || position.DishID == "" || position.Portions <= 0 {
			return nil, contract.ErrInvalidArgument
		}
		if _, exists := seen[position.LineNo]; exists {
			return nil, contract.ErrInvalidArgument
		}
		seen[position.LineNo] = struct{}{}
		result[index] = domain.AcceptedPosition{LineNo: position.LineNo, DishID: position.DishID, Portions: position.Portions}
	}
	return result, nil
}

func acceptedAsPositions(positions []domain.AcceptedPosition) []domain.Position {
	result := make([]domain.Position, len(positions))
	for index, position := range positions {
		result[index] = domain.Position(position)
	}
	return result
}

func sameAcceptedPositions(want, got []domain.AcceptedPosition) bool {
	if len(want) != len(got) {
		return false
	}
	for index := range want {
		if want[index] != got[index] {
			return false
		}
	}
	return true
}

func toContractOrderEvaluation(value domain.OrderEvaluation) contract.OrderEvaluation {
	result := contract.OrderEvaluation{
		EvaluatedAt:        value.EvaluatedAt,
		StateVersion:       value.StateVersion,
		RequestedPositions: make([]contract.Position, len(value.RequestedPositions)),
		AcceptedPositions:  make([]contract.AcceptedPosition, len(value.AcceptedPositions)),
		Changes:            make([]contract.Change, len(value.Changes)),
	}
	for index, position := range value.RequestedPositions {
		result.RequestedPositions[index] = contract.Position{LineNo: position.LineNo, DishID: position.DishID, Portions: position.Portions}
	}
	for index, position := range value.AcceptedPositions {
		result.AcceptedPositions[index] = contract.AcceptedPosition{LineNo: position.LineNo, DishID: position.DishID, Portions: position.Portions}
	}
	for index, change := range value.Changes {
		result.Changes[index] = contract.Change{LineNo: change.LineNo, DishID: change.DishID, RequestedPortions: change.RequestedPortions, AcceptedPortions: change.AcceptedPortions, Deficits: toContractDeficits(change.Deficits)}
	}
	return result
}

func toContractDeficits(values []domain.Deficit) []contract.Deficit {
	result := make([]contract.Deficit, len(values))
	for index, deficit := range values {
		result[index] = contract.Deficit{ProductID: deficit.ProductID, RequiredQuantity: contract.Quantity(deficit.RequiredQuantity), AvailableQuantity: contract.Quantity(deficit.AvailableQuantity)}
	}
	return result
}

func recordedCommitOrder(record domain.CommandRecord, fingerprint string) (contract.CommitResult, error) {
	if record.CommandType != "ConfirmOrder" || record.CommandFingerprint != fingerprint {
		return contract.CommitResult{}, contract.ErrCommandIdentityConflict
	}
	if record.Result.Kind != domain.CommandResultOrderPlaced || record.Result.Order == nil {
		return contract.CommitResult{}, domain.ErrInvalidRecordedBatch
	}
	order := toContractOrder(*record.Result.Order)
	return contract.CommitResult{Kind: contract.CommitResultOrderCommitted, Order: &order}, nil
}

func toContractOrder(value domain.Order) contract.Order {
	result := contract.Order{OrderID: value.OrderID, ProposalID: value.ProposalID, PlacedAt: value.PlacedAt, AcceptedPositions: make([]contract.AcceptedPosition, len(value.AcceptedPositions)), Allocations: make([]contract.Allocation, len(value.Allocations))}
	for index, position := range value.AcceptedPositions {
		result.AcceptedPositions[index] = contract.AcceptedPosition{LineNo: position.LineNo, DishID: position.DishID, Portions: position.Portions}
	}
	for index, allocation := range value.Allocations {
		result.Allocations[index] = contract.Allocation{ProductID: allocation.ProductID, BatchID: allocation.BatchID, Quantity: contract.Quantity(allocation.Quantity)}
	}
	return result
}

func publicEvaluationError(err error) error {
	switch {
	case errors.Is(err, domain.ErrDishNotFound):
		return contract.ErrDishNotFound
	case errors.Is(err, domain.ErrInvalidQuantity):
		return contract.ErrInvalidArgument
	default:
		return err
	}
}
