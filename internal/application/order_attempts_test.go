package application

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/JustIkra/umir-warehouse/contract"
	"github.com/JustIkra/umir-warehouse/internal/adapters/memory"
	"github.com/JustIkra/umir-warehouse/internal/domain"
)

type attemptOwner struct {
	mu      sync.Mutex
	started chan struct{}
	release chan struct{}
	calls   int
	result  contract.CommitResult
	err     error
	malform bool
}

func (o *attemptOwner) CommitAcceptedOrder(ctx context.Context, request contract.CommitOrderRequest) (contract.CommitResult, error) {
	o.mu.Lock()
	o.calls++
	o.mu.Unlock()
	if o.started != nil {
		close(o.started)
		select {
		case <-ctx.Done():
			return contract.CommitResult{}, ctx.Err()
		case <-o.release:
		}
	}
	if o.malform {
		return contract.CommitResult{}, nil
	}
	return o.result, o.err
}

func (o *attemptOwner) FindRecordedCommand(context.Context, string) (contract.CommandRecord, bool, error) {
	return contract.CommandRecord{}, false, nil
}

type appendThenUnknownStore struct {
	EventStore
	commandID string
	armed     bool
}

func (s *appendThenUnknownStore) AppendBatch(ctx context.Context, batch domain.CommandBatch) (domain.AppendResult, error) {
	result, err := s.EventStore.AppendBatch(ctx, batch)
	if err == nil && s.armed && batch.CommandID == s.commandID {
		s.armed = false
		return domain.AppendResult{}, domain.ErrUnknownWriteOutcome
	}
	return result, err
}

type countedCommitOwner struct {
	owner CommitOwner
	calls int
}

func (o *countedCommitOwner) CommitAcceptedOrder(ctx context.Context, request contract.CommitOrderRequest) (contract.CommitResult, error) {
	o.calls++
	return o.owner.CommitAcceptedOrder(ctx, request)
}

func TestOrderAttemptDuplicateDuringOwnerExecutionReturnsPending(t *testing.T) {
	owner := &attemptOwner{started: make(chan struct{}), release: make(chan struct{}), result: contract.CommitResult{Kind: contract.CommitResultRecheckRequired, Evaluation: &contract.OrderEvaluation{}}}
	coordinator := NewOrderAttemptCoordinator(owner)
	request := contract.CommitOrderRequest{CommandID: "c1", ProposalID: "p1", AcceptedPositions: []contract.AcceptedPosition{{LineNo: 1, DishID: "d1", Portions: 1}}}
	first := make(chan error, 1)
	go func() {
		_, err := coordinator.CommitAcceptedOrder(context.Background(), request)
		first <- err
	}()
	<-owner.started
	result, err := coordinator.CommitAcceptedOrder(context.Background(), request)
	if err != nil || result.Kind != contract.CommitResultPending {
		t.Fatalf("duplicate result = (%+v, %v), want Pending", result, err)
	}
	attempt, err := coordinator.FindOrderAttempt(context.Background(), request.CommandID, request.ProposalID)
	if err != nil || attempt.State != contract.OrderAttemptPending {
		t.Fatalf("attempt = (%+v, %v), want Pending", attempt, err)
	}
	close(owner.release)
	if err := <-first; err != nil {
		t.Fatalf("first result = %v", err)
	}
	owner.mu.Lock()
	calls := owner.calls
	owner.mu.Unlock()
	if calls != 1 {
		t.Fatalf("owner calls = %d, want 1", calls)
	}
}

func TestOrderAttemptCachesRecheckAndRejectsCrossProposalReuse(t *testing.T) {
	evaluation := contract.OrderEvaluation{StateVersion: 7, Changes: []contract.Change{{LineNo: 1, RequestedPortions: 3, AcceptedPortions: 2}}}
	owner := &attemptOwner{result: contract.CommitResult{Kind: contract.CommitResultRecheckRequired, Evaluation: &evaluation}}
	coordinator := NewOrderAttemptCoordinator(owner)
	request := contract.CommitOrderRequest{CommandID: "c1", ProposalID: "p1", AcceptedPositions: []contract.AcceptedPosition{{LineNo: 1, DishID: "d1", Portions: 2}}}
	first, err := coordinator.CommitAcceptedOrder(context.Background(), request)
	if err != nil || first.Kind != contract.CommitResultRecheckRequired {
		t.Fatalf("commit = (%+v, %v), want RecheckRequired", first, err)
	}
	first.Evaluation.Changes[0].AcceptedPortions = 99
	replay, err := coordinator.CommitAcceptedOrder(context.Background(), request)
	if err != nil || replay.Kind != contract.CommitResultRecheckRequired || replay.Evaluation.Changes[0].AcceptedPortions != 2 {
		t.Fatalf("replay = (%+v, %v), want immutable saved recheck", replay, err)
	}
	otherProposal := request
	otherProposal.ProposalID = "p2"
	_, err = coordinator.CommitAcceptedOrder(context.Background(), otherProposal)
	if !errors.Is(err, contract.ErrCommandIdentityConflict) {
		t.Fatalf("cross-proposal error = %v, want identity conflict", err)
	}
	owner.mu.Lock()
	calls := owner.calls
	owner.mu.Unlock()
	if calls != 1 {
		t.Fatalf("owner calls = %d, want 1", calls)
	}
}

func TestOrderAttemptCachesResultResolvedAfterUnknownAppend(t *testing.T) {
	ctx := context.Background()
	now := moscowTime(2026, time.October, 11, 12)
	store := &appendThenUnknownStore{EventStore: memory.NewEventStore()}
	id := 0
	owner := newOwnerForCatalogTest(t, store, now)
	owner.newID = func() string { id++; return fmt.Sprintf("event-%d", id) }
	if _, err := owner.RegisterProduct(ctx, contract.RegisterProductRequest{CommandID: "product", ProductID: "p1", Name: "Milk", BaseUnit: contract.BaseUnitML}); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.DefineDish(ctx, contract.DefineDishRequest{CommandID: "dish", DishID: "d1", Name: "Soup", Ingredients: []contract.Ingredient{{ProductID: "p1", Quantity: 1000}}}); err != nil {
		t.Fatal(err)
	}
	delivery := validDeliveryRequest("delivery", "delivery-1", now)
	delivery.Lines[0].ProductID = "p1"
	if _, err := owner.ReceiveDelivery(ctx, delivery); err != nil {
		t.Fatal(err)
	}
	committer := &countedCommitOwner{owner: owner}
	coordinator := NewOrderAttemptCoordinator(committer)
	request := contract.CommitOrderRequest{CommandID: "c1", ProposalID: "p1", AcceptedPositions: []contract.AcceptedPosition{{LineNo: 1, DishID: "d1", Portions: 1}}}
	store.commandID = request.CommandID
	store.armed = true
	first, err := coordinator.CommitAcceptedOrder(ctx, request)
	if err != nil || first.Kind != contract.CommitResultOrderCommitted {
		t.Fatalf("commit = (%+v, %v), want completed after Owner resolves unknown append", first, err)
	}
	version, err := store.Version(ctx)
	if err != nil {
		t.Fatal(err)
	}
	replay, err := coordinator.CommitAcceptedOrder(ctx, request)
	if err != nil || replay.Order == nil || replay.Order.OrderID != first.Order.OrderID {
		t.Fatalf("replay = (%+v, %v), want original result", replay, err)
	}
	if committer.calls != 1 {
		t.Fatalf("Owner calls = %d, want only the initial call", committer.calls)
	}
	versionAfter, err := store.Version(ctx)
	if err != nil || versionAfter != version {
		t.Fatalf("version after replay = %d, %v; want %d", versionAfter, err, version)
	}
}

func TestOrderAttemptCachesDefiniteRefusalAndPassiveLookupDoesNotCommit(t *testing.T) {
	owner := &attemptOwner{err: contract.ErrInsufficientInventory}
	coordinator := NewOrderAttemptCoordinator(owner)
	request := contract.CommitOrderRequest{CommandID: "c1", ProposalID: "p1", AcceptedPositions: []contract.AcceptedPosition{{LineNo: 1, DishID: "d1", Portions: 1}}}
	_, firstErr := coordinator.CommitAcceptedOrder(context.Background(), request)
	if !errors.Is(firstErr, contract.ErrInsufficientInventory) {
		t.Fatalf("commit error = %v, want insufficient inventory", firstErr)
	}
	_, replayErr := coordinator.CommitAcceptedOrder(context.Background(), request)
	if !errors.Is(replayErr, contract.ErrInsufficientInventory) {
		t.Fatalf("replay error = %v, want saved refusal", replayErr)
	}
	attempt, err := coordinator.FindOrderAttempt(context.Background(), request.CommandID, request.ProposalID)
	if err != nil || attempt.State != contract.OrderAttemptRejected || attempt.Error == nil || attempt.Error.Outcome != contract.OutcomeNotCommitted {
		t.Fatalf("attempt = (%+v, %v), want Rejected/not_committed", attempt, err)
	}
	_, err = coordinator.CommitAcceptedOrder(context.Background(), contract.CommitOrderRequest{CommandID: "c2", ProposalID: "p1", AcceptedPositions: request.AcceptedPositions})
	if !errors.Is(err, contract.ErrInsufficientInventory) {
		t.Fatalf("new command error = %v, want owner refusal", err)
	}
	owner.mu.Lock()
	calls := owner.calls
	owner.mu.Unlock()
	if calls != 2 {
		t.Fatalf("owner calls = %d, want one per command", calls)
	}
}

func TestOrderAttemptUncertainOwnerCompletionCanRetryOnlyAfterPreviousCallEnds(t *testing.T) {
	owner := &attemptOwner{err: errors.Join(contract.ErrUnknownWriteOutcome, contract.ErrInvalidArgument)}
	coordinator := NewOrderAttemptCoordinator(owner)
	request := contract.CommitOrderRequest{CommandID: "c1", ProposalID: "p1", AcceptedPositions: []contract.AcceptedPosition{{LineNo: 1, DishID: "d1", Portions: 1}}}
	if _, err := coordinator.CommitAcceptedOrder(context.Background(), request); err == nil {
		t.Fatal("uncertain owner call unexpectedly succeeded")
	}
	attempt, err := coordinator.FindOrderAttempt(context.Background(), request.CommandID, request.ProposalID)
	if err != nil || attempt.State != contract.OrderAttemptPending {
		t.Fatalf("attempt = (%+v, %v), want Pending after uncertainty", attempt, err)
	}
	owner.err = nil
	owner.result = contract.CommitResult{Kind: contract.CommitResultOrderCommitted, Order: &contract.Order{OrderID: "o1", ProposalID: "p1"}}
	result, err := coordinator.CommitAcceptedOrder(context.Background(), request)
	if err != nil || result.Kind != contract.CommitResultOrderCommitted {
		t.Fatalf("retry = (%+v, %v), want saved owner completion", result, err)
	}
}

func TestWrappedDefiniteSentinelDoesNotBecomeTerminalRefusal(t *testing.T) {
	owner := &attemptOwner{err: fmt.Errorf("uncertain wrapper: %w", contract.ErrInvalidArgument)}
	coordinator := NewOrderAttemptCoordinator(owner)
	request := contract.CommitOrderRequest{CommandID: "c1", ProposalID: "p1", AcceptedPositions: []contract.AcceptedPosition{{LineNo: 1, DishID: "d1", Portions: 1}}}
	if _, err := coordinator.CommitAcceptedOrder(context.Background(), request); err == nil {
		t.Fatal("uncertain wrapped owner error unexpectedly succeeded")
	}
	attempt, err := coordinator.FindOrderAttempt(context.Background(), request.CommandID, request.ProposalID)
	if err != nil || attempt.State != contract.OrderAttemptPending {
		t.Fatalf("attempt = (%+v, %v), want Pending for wrapped uncertainty", attempt, err)
	}
}

func TestOrderAttemptUnseenAndWrongProposalLookupArePassive(t *testing.T) {
	owner := &attemptOwner{err: errors.New("owner result not configured")}
	coordinator := NewOrderAttemptCoordinator(owner)
	attempt, err := coordinator.FindOrderAttempt(context.Background(), "c1", "p1")
	if err != nil || attempt.State != contract.OrderAttemptUnseen {
		t.Fatalf("lookup = (%+v, %v), want Unseen", attempt, err)
	}
	_, err = coordinator.CommitAcceptedOrder(context.Background(), contract.CommitOrderRequest{CommandID: "c1", ProposalID: "p1", AcceptedPositions: []contract.AcceptedPosition{{LineNo: 1, DishID: "d1", Portions: 1}}})
	if err == nil {
		t.Fatal("owner result unexpectedly succeeded")
	}
	_, err = coordinator.FindOrderAttempt(context.Background(), "c1", "p2")
	if !errors.Is(err, contract.ErrCommandIdentityConflict) {
		t.Fatalf("wrong-proposal lookup error = %v, want identity conflict", err)
	}
	owner.mu.Lock()
	calls := owner.calls
	owner.mu.Unlock()
	if calls != 1 {
		t.Fatalf("lookup changed owner call count to %d", calls)
	}
}

func TestOtherCommandReservationsSerializeExactRetries(t *testing.T) {
	coordinator := NewOrderAttemptCoordinator(&attemptOwner{})
	started := make(chan struct{}, 2)
	release := make(chan struct{})
	results := make(chan error, 2)
	run := func() error {
		started <- struct{}{}
		<-release
		return nil
	}
	go func() { results <- coordinator.RunOtherCommand(context.Background(), "c1", run) }()
	<-started
	secondReady := make(chan struct{})
	go func() {
		close(secondReady)
		results <- coordinator.RunOtherCommand(context.Background(), "c1", run)
	}()
	<-secondReady
	close(release)
	<-started
	if err := <-results; err != nil {
		t.Fatalf("first command = %v", err)
	}
	if err := <-results; err != nil {
		t.Fatalf("exact retry = %v", err)
	}
}

func TestInvalidOwnerCommitShapeRemainsPending(t *testing.T) {
	owner := &attemptOwner{malform: true}
	coordinator := NewOrderAttemptCoordinator(owner)
	request := contract.CommitOrderRequest{CommandID: "c1", ProposalID: "p1", AcceptedPositions: []contract.AcceptedPosition{{LineNo: 1, DishID: "d1", Portions: 1}}}
	if _, err := coordinator.CommitAcceptedOrder(context.Background(), request); err == nil {
		t.Fatal("malformed successful owner result unexpectedly completed")
	}
	attempt, err := coordinator.FindOrderAttempt(context.Background(), request.CommandID, request.ProposalID)
	if err != nil || attempt.State != contract.OrderAttemptPending {
		t.Fatalf("attempt = (%+v, %v), want Pending", attempt, err)
	}
}

func TestW6CommandOnlyAttemptUsesRetainedCoordinator(t *testing.T) {
	ctx := context.Background()
	owner := &attemptOwner{started: make(chan struct{}), release: make(chan struct{}), result: contract.CommitResult{Kind: contract.CommitResultRecheckRequired, Evaluation: &contract.OrderEvaluation{StateVersion: 7}}}
	c := NewOrderAttemptCoordinator(owner)
	if v, err := c.FindOrderAttemptByCommand(ctx, "absent"); err != nil || v.State != contract.OrderAttemptUnseen {
		t.Fatal(v, err)
	}
	request := contract.CommitOrderRequest{CommandID: "c", ProposalID: "p", AcceptedPositions: []contract.AcceptedPosition{{LineNo: 1, DishID: "D", Portions: 1}}}
	done := make(chan error, 1)
	go func() { _, err := c.CommitAcceptedOrder(ctx, request); done <- err }()
	<-owner.started
	v, err := c.FindOrderAttemptByCommand(ctx, "c")
	if err != nil || v.State != contract.OrderAttemptPending {
		t.Fatal(v, err)
	}
	close(owner.release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	v, err = c.FindOrderAttemptByCommand(ctx, "c")
	if err != nil || v.State != contract.OrderAttemptCompleted || v.Result.Evaluation.StateVersion != 7 {
		t.Fatal(v, err)
	}
	v.Result.Evaluation.StateVersion = 99
	v, err = c.FindOrderAttemptByCommand(ctx, "c")
	if err != nil || v.Result.Evaluation.StateVersion != 7 {
		t.Fatal("retained alias", v, err)
	}
	if _, err = c.FindOrderAttempt(ctx, "c", "wrong"); !errors.Is(err, contract.ErrCommandIdentityConflict) {
		t.Fatal("legacy proposal check", err)
	}
	rejected := NewOrderAttemptCoordinator(&attemptOwner{err: contract.ErrDishNotFound})
	if _, err = rejected.CommitAcceptedOrder(ctx, request); !errors.Is(err, contract.ErrDishNotFound) {
		t.Fatal(err)
	}
	v, err = rejected.FindOrderAttemptByCommand(ctx, "c")
	if err != nil || v.State != contract.OrderAttemptRejected || v.Error.Outcome != contract.OutcomeNotCommitted {
		t.Fatal(v, err)
	}
}
