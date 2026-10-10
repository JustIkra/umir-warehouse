package application

import (
	"context"
	"errors"
	"sync"

	"github.com/JustIkra/umir-warehouse/contract"
)

type CommitOwner interface {
	CommitAcceptedOrder(context.Context, contract.CommitOrderRequest) (contract.CommitResult, error)
}

type RecordedCommandReader interface {
	FindRecordedCommand(context.Context, string) (contract.CommandRecord, bool, error)
}

type OrderAttemptCoordinator struct {
	mu       sync.Mutex
	owner    CommitOwner
	reader   RecordedCommandReader
	attempts map[string]*orderAttemptEntry
	other    map[string]chan struct{}
}

type orderAttemptEntry struct {
	mu        sync.Mutex
	proposal  string
	positions []contract.AcceptedPosition
	running   bool
	state     contract.OrderAttemptState
	result    contract.CommitResult
	failure   contract.OrderAttemptError
	err       error
}

func NewOrderAttemptCoordinator(owner CommitOwner, reader ...RecordedCommandReader) *OrderAttemptCoordinator {
	coordinator := &OrderAttemptCoordinator{owner: owner, attempts: make(map[string]*orderAttemptEntry), other: make(map[string]chan struct{})}
	if len(reader) != 0 {
		coordinator.reader = reader[0]
	}
	return coordinator
}

func (c *OrderAttemptCoordinator) CommitAcceptedOrder(ctx context.Context, request contract.CommitOrderRequest) (contract.CommitResult, error) {
	if c == nil || c.owner == nil || !validAttemptRequest(request) {
		return contract.CommitResult{}, contract.ErrInvalidArgument
	}
	var entry *orderAttemptEntry
	for {
		c.mu.Lock()
		if active, reserved := c.other[request.CommandID]; reserved {
			c.mu.Unlock()
			select {
			case <-ctx.Done():
				return contract.CommitResult{}, ctx.Err()
			case <-active:
				continue
			}
		}
		var exists bool
		entry, exists = c.attempts[request.CommandID]
		if !exists {
			entry = &orderAttemptEntry{proposal: request.ProposalID, positions: cloneAcceptedPositions(request.AcceptedPositions), state: contract.OrderAttemptPending}
			c.attempts[request.CommandID] = entry
		}
		entry.mu.Lock()
		c.mu.Unlock()
		if entry.proposal != request.ProposalID || !equalAcceptedPositions(entry.positions, request.AcceptedPositions) {
			entry.mu.Unlock()
			return contract.CommitResult{}, contract.ErrCommandIdentityConflict
		}
		break
	}
	switch {
	case entry.state == contract.OrderAttemptCompleted:
		result := cloneCommitResult(entry.result)
		entry.mu.Unlock()
		return result, nil
	case entry.state == contract.OrderAttemptRejected:
		err := entry.err
		entry.mu.Unlock()
		return contract.CommitResult{}, err
	case entry.running:
		entry.mu.Unlock()
		return contract.CommitResult{Kind: contract.CommitResultPending}, nil
	}
	entry.running = true
	entry.state = contract.OrderAttemptPending
	entry.mu.Unlock()
	if c.reader != nil {
		record, found, lookupErr := c.reader.FindRecordedCommand(ctx, request.CommandID)
		if lookupErr != nil {
			entry.mu.Lock()
			entry.running = false
			entry.err = lookupErr
			entry.mu.Unlock()
			return contract.CommitResult{}, lookupErr
		}
		if found && (record.CommandType != "ConfirmOrder" || record.CommandFingerprint != canonicalConfirmOrderFP(request.ProposalID)) {
			c.mu.Lock()
			if c.attempts[request.CommandID] == entry {
				delete(c.attempts, request.CommandID)
			}
			c.mu.Unlock()
			entry.mu.Lock()
			entry.running = false
			entry.mu.Unlock()
			return contract.CommitResult{}, contract.ErrCommandIdentityConflict
		}
	}
	result, err := c.owner.CommitAcceptedOrder(ctx, cloneCommitRequest(request))
	entry.mu.Lock()
	entry.running = false
	if err == nil {
		if !validCommitResult(result) {
			entry.state = contract.OrderAttemptPending
			entry.err = errors.New("owner returned an invalid commit result")
			entry.mu.Unlock()
			return contract.CommitResult{}, entry.err
		}
		entry.state = contract.OrderAttemptCompleted
		entry.result = cloneCommitResult(result)
		result := cloneCommitResult(entry.result)
		entry.mu.Unlock()
		return result, nil
	}
	if failure, definite := definiteOrderRefusal(err); definite {
		entry.state = contract.OrderAttemptRejected
		entry.failure = failure
		entry.err = err
		entry.mu.Unlock()
		return contract.CommitResult{}, err
	}
	entry.state = contract.OrderAttemptPending
	entry.err = err
	entry.mu.Unlock()
	return contract.CommitResult{}, err
}

func (c *OrderAttemptCoordinator) RunOtherCommand(ctx context.Context, commandID string, run func() error) error {
	if c == nil || run == nil || commandID == "" {
		return contract.ErrInvalidArgument
	}
	for {
		c.mu.Lock()
		if _, exists := c.attempts[commandID]; exists {
			c.mu.Unlock()
			return contract.ErrCommandIdentityConflict
		}
		if active, exists := c.other[commandID]; exists {
			c.mu.Unlock()
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-active:
				continue
			}
		}
		active := make(chan struct{})
		c.other[commandID] = active
		c.mu.Unlock()
		defer func() {
			c.mu.Lock()
			delete(c.other, commandID)
			close(active)
			c.mu.Unlock()
		}()
		return run()
	}
}

func (c *OrderAttemptCoordinator) FindOrderAttempt(ctx context.Context, commandID, proposalID string) (contract.OrderAttempt, error) {
	if err := ctx.Err(); err != nil {
		return contract.OrderAttempt{}, err
	}
	if commandID == "" || proposalID == "" {
		return contract.OrderAttempt{}, contract.ErrInvalidArgument
	}
	c.mu.Lock()
	entry, found := c.attempts[commandID]
	if !found {
		c.mu.Unlock()
		return contract.OrderAttempt{State: contract.OrderAttemptUnseen}, nil
	}
	entry.mu.Lock()
	c.mu.Unlock()
	defer entry.mu.Unlock()
	if entry.proposal != proposalID {
		return contract.OrderAttempt{}, contract.ErrCommandIdentityConflict
	}
	if entry.running || entry.state == contract.OrderAttemptPending {
		return contract.OrderAttempt{State: contract.OrderAttemptPending}, nil
	}
	if entry.state == contract.OrderAttemptRejected {
		failure := entry.failure
		return contract.OrderAttempt{State: contract.OrderAttemptRejected, Error: &failure}, nil
	}
	result := cloneCommitResult(entry.result)
	return contract.OrderAttempt{State: contract.OrderAttemptCompleted, Result: &result}, nil
}

func validAttemptRequest(request contract.CommitOrderRequest) bool {
	if request.CommandID == "" || isReservedCommandID(request.CommandID) || request.ProposalID == "" || len(request.AcceptedPositions) == 0 {
		return false
	}
	seen := make(map[int64]struct{}, len(request.AcceptedPositions))
	for _, position := range request.AcceptedPositions {
		if position.LineNo <= 0 || position.DishID == "" || position.Portions <= 0 {
			return false
		}
		if _, exists := seen[position.LineNo]; exists {
			return false
		}
		seen[position.LineNo] = struct{}{}
	}
	return true
}

func validCommitResult(value contract.CommitResult) bool {
	switch value.Kind {
	case contract.CommitResultOrderCommitted:
		return value.Order != nil && value.Evaluation == nil
	case contract.CommitResultRecheckRequired:
		return value.Evaluation != nil && value.Order == nil
	default:
		return false
	}
}

func definiteOrderRefusal(err error) (contract.OrderAttemptError, bool) {
	var code, message string
	switch {
	case exactError(err, contract.ErrInvalidArgument):
		code, message = "InvalidArgument", "InvalidArgument: commit fields are invalid."
	case exactError(err, contract.ErrCommandIdentityConflict):
		code, message = "CommandIdentityConflict", "CommandIdentityConflict: commandID is bound to another command."
	case exactError(err, contract.ErrDeliveryAlreadyReceived):
		code, message = "DeliveryAlreadyReceived", "DeliveryAlreadyReceived: deliveryID was already accepted."
	case exactError(err, contract.ErrProductNotFound):
		code, message = "ProductNotFound", "ProductNotFound: product does not exist."
	case exactError(err, contract.ErrDishNotFound):
		code, message = "DishNotFound", "DishNotFound: dish does not exist."
	case exactError(err, contract.ErrProductAlreadyExists):
		code, message = "ProductAlreadyExists", "ProductAlreadyExists: productID is already registered."
	case exactError(err, contract.ErrDishAlreadyExists):
		code, message = "DishAlreadyExists", "DishAlreadyExists: dishID is already defined."
	case exactError(err, contract.ErrInsufficientInventory):
		code, message = "InsufficientInventory", "InsufficientInventory: the requested order cannot be committed."
	case exactError(err, contract.ErrNotDue):
		code, message = "NotDue", "NotDue: batch expiration is not due."
	default:
		if errors.Is(err, contract.ErrUnknownWriteOutcome) || errors.Is(err, contract.ErrInventoryUnavailable) || errors.Is(err, contract.ErrConcurrentModification) {
			return contract.OrderAttemptError{}, false
		}
		return contract.OrderAttemptError{}, false
	}
	return contract.OrderAttemptError{Code: code, Message: message, Outcome: contract.OutcomeNotCommitted}, true
}

func exactError(err, target error) bool {
	var wrapped interface{ Unwrap() error }
	var joined interface{ Unwrap() []error }
	if errors.As(err, &wrapped) || errors.As(err, &joined) {
		return false
	}
	return errors.Is(err, target)
}

func cloneCommitRequest(request contract.CommitOrderRequest) contract.CommitOrderRequest {
	request.AcceptedPositions = cloneAcceptedPositions(request.AcceptedPositions)
	return request
}

func cloneAcceptedPositions(values []contract.AcceptedPosition) []contract.AcceptedPosition {
	return append([]contract.AcceptedPosition(nil), values...)
}

func equalAcceptedPositions(left, right []contract.AcceptedPosition) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func cloneCommitResult(value contract.CommitResult) contract.CommitResult {
	result := contract.CommitResult{Kind: value.Kind}
	if value.Order != nil {
		order := *value.Order
		order.AcceptedPositions = cloneAcceptedPositions(value.Order.AcceptedPositions)
		order.Allocations = append([]contract.Allocation(nil), value.Order.Allocations...)
		result.Order = &order
	}
	if value.Evaluation != nil {
		evaluation := *value.Evaluation
		evaluation.RequestedPositions = append([]contract.Position(nil), value.Evaluation.RequestedPositions...)
		evaluation.AcceptedPositions = cloneAcceptedPositions(value.Evaluation.AcceptedPositions)
		evaluation.Changes = make([]contract.Change, len(value.Evaluation.Changes))
		for index, change := range value.Evaluation.Changes {
			evaluation.Changes[index] = change
			evaluation.Changes[index].Deficits = append([]contract.Deficit(nil), change.Deficits...)
		}
		result.Evaluation = &evaluation
	}
	return result
}
