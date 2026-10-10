package application

import (
	"context"
	"errors"
	"math"
	"reflect"
	"testing"
	"time"

	"github.com/JustIkra/umir-warehouse/contract"
	"github.com/JustIkra/umir-warehouse/internal/domain"
)

func TestSharedIngredientOrder(t *testing.T) {
	ctx := context.Background()
	now := moscowTime(2026, time.April, 2, 10)
	owner, _, _ := newCatalogOwner(t, now)
	if _, err := owner.RegisterProduct(ctx, contract.RegisterProductRequest{CommandID: "product-cheese", ProductID: "cheese", Name: "Cheese", BaseUnit: contract.BaseUnitG}); err != nil {
		t.Fatal(err)
	}
	for _, dish := range []contract.DefineDishRequest{
		{CommandID: "dish-a", DishID: "a", Name: "Dish A", Ingredients: []contract.Ingredient{{ProductID: "cheese", Quantity: 40000}}},
		{CommandID: "dish-b", DishID: "b", Name: "Dish B", Ingredients: []contract.Ingredient{{ProductID: "cheese", Quantity: 40000}}},
	} {
		if _, err := owner.DefineDish(ctx, dish); err != nil {
			t.Fatal(err)
		}
	}
	delivery := validDeliveryRequest("delivery-cheese", "cheese-stock", now)
	delivery.Lines[0].ProductID = "cheese"
	delivery.Lines[0].Quantity = 40000
	if _, err := owner.ReceiveDelivery(ctx, delivery); err != nil {
		t.Fatal(err)
	}
	menu, err := owner.EvaluateMenu(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(menu.Dishes) != 2 || !menu.Dishes[0].IsAvailable || !menu.Dishes[1].IsAvailable {
		t.Fatalf("independent menu availability = %#v; want both dishes available", menu.Dishes)
	}
	evaluation, err := owner.EvaluateOrder(ctx, contract.EvaluateOrderRequest{Positions: []contract.Position{{LineNo: 4, DishID: "a", Portions: 1}, {LineNo: 9, DishID: "b", Portions: 1}}})
	if err != nil {
		t.Fatal(err)
	}
	wantAccepted := []contract.AcceptedPosition{{LineNo: 4, DishID: "a", Portions: 1}}
	if !reflect.DeepEqual(evaluation.AcceptedPositions, wantAccepted) {
		t.Fatalf("accepted positions = %#v; want %#v", evaluation.AcceptedPositions, wantAccepted)
	}
	if len(evaluation.Changes) != 1 || evaluation.Changes[0].LineNo != 9 || evaluation.Changes[0].AcceptedPortions != 0 {
		t.Fatalf("changes = %#v; want line 9 reduced to zero", evaluation.Changes)
	}
	if deficit := evaluation.Changes[0].Deficits[0]; deficit.ProductID != "cheese" || deficit.RequiredQuantity != 40000 || deficit.AvailableQuantity != 0 {
		t.Fatalf("second-line deficit = %#v; want 40000 required and none available", deficit)
	}
}

func TestIndependentMenu(t *testing.T) {
	ctx := context.Background()
	now := moscowTime(2026, time.April, 2, 10)
	owner, _, _ := newCatalogOwner(t, now)
	if _, err := owner.RegisterProduct(ctx, contract.RegisterProductRequest{CommandID: "product-p", ProductID: "p", Name: "Pear", BaseUnit: contract.BaseUnitG}); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.RegisterProduct(ctx, contract.RegisterProductRequest{CommandID: "product-q", ProductID: "q", Name: "Quince", BaseUnit: contract.BaseUnitG}); err != nil {
		t.Fatal(err)
	}
	for _, dish := range []contract.DefineDishRequest{
		{CommandID: "dish-b", DishID: "b", Name: "B", Ingredients: []contract.Ingredient{{ProductID: "p", Quantity: 40000}}},
		{CommandID: "dish-a", DishID: "a", Name: "A", Ingredients: []contract.Ingredient{{ProductID: "p", Quantity: 40000}}},
		{CommandID: "dish-c", DishID: "c", Name: "C", Ingredients: []contract.Ingredient{{ProductID: "q", Quantity: 500}}},
	} {
		if _, err := owner.DefineDish(ctx, dish); err != nil {
			t.Fatal(err)
		}
	}
	delivery := validDeliveryRequest("delivery-p", "delivery-1", now)
	delivery.Lines[0].Quantity = 40000
	if _, err := owner.ReceiveDelivery(ctx, delivery); err != nil {
		t.Fatal(err)
	}
	menu, err := owner.EvaluateMenu(ctx)
	if err != nil {
		t.Fatal(err)
	}
	want := contract.MenuEvaluation{
		EvaluatedAt:  now,
		StateVersion: 6,
		Dishes: []contract.DishEvaluation{
			{DishID: "a", DishName: "A", IsAvailable: true, Deficits: []contract.Deficit{}, Allocations: []contract.MenuAllocation{{ProductID: "p", ProductName: "Pear", BatchID: "owner-event-6", Quantity: 40000, BaseUnit: contract.BaseUnitG, ExpiresAt: moscowTime(2026, time.April, 4, 10)}}},
			{DishID: "b", DishName: "B", IsAvailable: true, Deficits: []contract.Deficit{}, Allocations: []contract.MenuAllocation{{ProductID: "p", ProductName: "Pear", BatchID: "owner-event-6", Quantity: 40000, BaseUnit: contract.BaseUnitG, ExpiresAt: moscowTime(2026, time.April, 4, 10)}}},
			{DishID: "c", DishName: "C", IsAvailable: false, Deficits: []contract.Deficit{{ProductID: "q", RequiredQuantity: 500, AvailableQuantity: 0}}, Allocations: []contract.MenuAllocation{}},
		},
	}
	if !reflect.DeepEqual(menu, want) {
		t.Fatalf("EvaluateMenu() = %#v; want %#v", menu, want)
	}
}

func TestCommitRecheck(t *testing.T) {
	ctx := context.Background()
	now := moscowTime(2026, time.April, 2, 10)
	owner, _, _ := newCatalogOwner(t, now)
	if _, err := owner.RegisterProduct(ctx, contract.RegisterProductRequest{CommandID: "product-p", ProductID: "p", Name: "Pear", BaseUnit: contract.BaseUnitG}); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.DefineDish(ctx, contract.DefineDishRequest{CommandID: "dish-d", DishID: "d", Name: "Dish", Ingredients: []contract.Ingredient{{ProductID: "p", Quantity: 40000}}}); err != nil {
		t.Fatal(err)
	}
	delivery := validDeliveryRequest("delivery-p", "delivery-1", now)
	delivery.Lines[0].Quantity = 80000
	if _, err := owner.ReceiveDelivery(ctx, delivery); err != nil {
		t.Fatal(err)
	}
	first, err := owner.CommitAcceptedOrder(ctx, contract.CommitOrderRequest{CommandID: "order-one", ProposalID: "proposal-one", AcceptedPositions: []contract.AcceptedPosition{{LineNo: 1, DishID: "d", Portions: 1}}})
	if err != nil || first.Kind != contract.CommitResultOrderCommitted || first.Order == nil {
		t.Fatalf("first commit = %#v, %v; want committed order", first, err)
	}
	wantAllocations := []contract.Allocation{{ProductID: "p", BatchID: "owner-event-3", Quantity: 40000}}
	if !reflect.DeepEqual(first.Order.Allocations, wantAllocations) {
		t.Fatalf("first order allocations = %#v; want %#v", first.Order.Allocations, wantAllocations)
	}
	result, err := owner.CommitAcceptedOrder(ctx, contract.CommitOrderRequest{CommandID: "order-two", ProposalID: "proposal-two", AcceptedPositions: []contract.AcceptedPosition{{LineNo: 7, DishID: "d", Portions: 2}}})
	if err != nil {
		t.Fatal(err)
	}
	if result.Kind != contract.CommitResultRecheckRequired || result.Order != nil || result.Evaluation == nil {
		t.Fatalf("rechecked commit = %#v; want RecheckRequired without an order", result)
	}
	if len(result.Evaluation.AcceptedPositions) != 1 || result.Evaluation.AcceptedPositions[0].Portions != 1 || len(result.Evaluation.Changes) != 1 || result.Evaluation.Changes[0].LineNo != 7 {
		t.Fatalf("recheck evaluation = %#v; want original line 7 reduced to one", result.Evaluation)
	}
	if deficit := result.Evaluation.Changes[0].Deficits[0]; deficit.ProductID != "p" || deficit.RequiredQuantity != 80000 || deficit.AvailableQuantity != 40000 {
		t.Fatalf("recheck deficit = %#v; want 80000 required and 40000 available", deficit)
	}
}

func TestKnownOrderReturnsBeforeProjectionRecovery(t *testing.T) {
	ctx := context.Background()
	now := moscowTime(2026, time.April, 2, 10)
	owner, original := committedOrderOwner(t, now)
	owner.events = &failingVersionEventStore{EventStore: owner.events}
	owner.usable = false
	replayed, err := owner.CommitAcceptedOrder(ctx, contract.CommitOrderRequest{CommandID: "order", ProposalID: "proposal", AcceptedPositions: []contract.AcceptedPosition{{LineNo: 1, DishID: "d", Portions: 99}}})
	if err != nil || replayed.Kind != contract.CommitResultOrderCommitted || replayed.Order == nil {
		t.Fatalf("known command retry = %#v, %v; want recorded order before projection recovery", replayed, err)
	}
	if !reflect.DeepEqual(replayed, original) {
		t.Fatalf("known order = %#v; want original %#v", replayed, original)
	}
}

func TestKnownProposalReturnsBeforeProjectionRecovery(t *testing.T) {
	ctx := context.Background()
	now := moscowTime(2026, time.April, 2, 10)
	owner, original := committedOrderOwner(t, now)
	owner.events = &failingVersionEventStore{EventStore: owner.events}
	owner.usable = false
	result, err := owner.CommitAcceptedOrder(ctx, contract.CommitOrderRequest{CommandID: "another-command", ProposalID: "proposal"})
	if err != nil || result.Kind != contract.CommitResultOrderCommitted || result.Order == nil || !reflect.DeepEqual(result, original) {
		t.Fatalf("known proposal result = %#v, %v; want recorded order before projection recovery", result, err)
	}
}

func committedOrderOwner(t *testing.T, now time.Time) (*Owner, contract.CommitResult) {
	t.Helper()
	ctx := context.Background()
	owner, _, _ := newCatalogOwner(t, now)
	if _, err := owner.RegisterProduct(ctx, contract.RegisterProductRequest{CommandID: "product-p", ProductID: "p", Name: "Pear", BaseUnit: contract.BaseUnitG}); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.DefineDish(ctx, contract.DefineDishRequest{CommandID: "dish-d", DishID: "d", Name: "Dish", Ingredients: []contract.Ingredient{{ProductID: "p", Quantity: 6000}}}); err != nil {
		t.Fatal(err)
	}
	delivery := validDeliveryRequest("delivery-p", "delivery-1", now)
	delivery.Lines[0].Quantity = 6000
	if _, err := owner.ReceiveDelivery(ctx, delivery); err != nil {
		t.Fatal(err)
	}
	result, err := owner.CommitAcceptedOrder(ctx, contract.CommitOrderRequest{CommandID: "order", ProposalID: "proposal", AcceptedPositions: []contract.AcceptedPosition{{LineNo: 1, DishID: "d", Portions: 1}}})
	if err != nil {
		t.Fatal(err)
	}
	return owner, result
}

func TestCommitRejectsEmptyAcceptedPositions(t *testing.T) {
	ctx := context.Background()
	now := moscowTime(2026, time.April, 2, 10)
	owner, store, _ := newCatalogOwner(t, now)
	if _, err := owner.CommitAcceptedOrder(ctx, contract.CommitOrderRequest{CommandID: "empty-order", ProposalID: "proposal"}); !errors.Is(err, contract.ErrInvalidArgument) {
		t.Fatalf("CommitAcceptedOrder(empty) error = %v; want InvalidArgument", err)
	}
	version, err := store.Version(ctx)
	if err != nil || version != 0 {
		t.Fatalf("version after empty commit = %d, %v; want zero", version, err)
	}
}

func TestCommitRejectsQuantityOverflowWithoutSale(t *testing.T) {
	ctx := context.Background()
	now := moscowTime(2026, time.April, 2, 10)
	owner, store, _ := newCatalogOwner(t, now)
	if _, err := owner.RegisterProduct(ctx, contract.RegisterProductRequest{CommandID: "product-p", ProductID: "p", Name: "Pear", BaseUnit: contract.BaseUnitG}); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.DefineDish(ctx, contract.DefineDishRequest{CommandID: "dish-d", DishID: "d", Name: "Dish", Ingredients: []contract.Ingredient{{ProductID: "p", Quantity: 2}}}); err != nil {
		t.Fatal(err)
	}
	delivery := validDeliveryRequest("delivery-p", "delivery-1", now)
	delivery.Lines[0].Quantity = contract.Quantity(math.MaxInt64)
	if _, err := owner.ReceiveDelivery(ctx, delivery); err != nil {
		t.Fatal(err)
	}
	_, err := owner.CommitAcceptedOrder(ctx, contract.CommitOrderRequest{CommandID: "overflow-order", ProposalID: "overflow-proposal", AcceptedPositions: []contract.AcceptedPosition{{LineNo: 1, DishID: "d", Portions: math.MaxInt64}}})
	if !errors.Is(err, domain.ErrQuantityOverflow) {
		t.Fatalf("CommitAcceptedOrder(overflow) error = %v; want ErrQuantityOverflow", err)
	}
	version, err := store.Version(ctx)
	if err != nil || version != 3 {
		t.Fatalf("version after rejected overflow = %d, %v; want only setup events", version, err)
	}
}

func TestProposalOrderUniqueness(t *testing.T) {
	ctx := context.Background()
	now := moscowTime(2026, time.April, 2, 10)
	owner, _, _ := newCatalogOwner(t, now)
	if _, err := owner.RegisterProduct(ctx, contract.RegisterProductRequest{CommandID: "product-p", ProductID: "p", Name: "Pear", BaseUnit: contract.BaseUnitG}); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.DefineDish(ctx, contract.DefineDishRequest{CommandID: "dish-d", DishID: "d", Name: "Dish", Ingredients: []contract.Ingredient{{ProductID: "p", Quantity: 1000}}}); err != nil {
		t.Fatal(err)
	}
	delivery := validDeliveryRequest("delivery-p", "delivery-1", now)
	delivery.Lines[0].Quantity = 5000
	if _, err := owner.ReceiveDelivery(ctx, delivery); err != nil {
		t.Fatal(err)
	}
	accepted := []contract.AcceptedPosition{{LineNo: 1, DishID: "d", Portions: 1}}
	first, err := owner.CommitAcceptedOrder(ctx, contract.CommitOrderRequest{CommandID: "order-one", ProposalID: "same-proposal", AcceptedPositions: accepted})
	if err != nil || first.Order == nil {
		t.Fatalf("first commit = %#v, %v; want committed order", first, err)
	}
	second, err := owner.CommitAcceptedOrder(ctx, contract.CommitOrderRequest{CommandID: "order-two", ProposalID: "same-proposal", AcceptedPositions: []contract.AcceptedPosition{{LineNo: 1, DishID: "d", Portions: 5}}})
	if err != nil || second.Order == nil || !reflect.DeepEqual(*second.Order, *first.Order) {
		t.Fatalf("second commit = %#v, %v; want original order %#v", second, err, *first.Order)
	}
}

func TestPendingProposalReturnsOriginalOrderWithExhaustedStock(t *testing.T) {
	testPendingProposalReturnsOriginalOrder(t, 40000)
}

func TestPendingProposalReturnsOriginalOrderWithRemainingStock(t *testing.T) {
	testPendingProposalReturnsOriginalOrder(t, 80000)
}

func testPendingProposalReturnsOriginalOrder(t *testing.T, stock contract.Quantity) {
	t.Helper()
	ctx := context.Background()
	now := moscowTime(2026, time.April, 2, 10)
	owner, store := newOrderCommitFixture(t, now, stock)
	wrapper := &delayConfirmOrderEventStore{EventStore: store, unknownAttempts: 2}
	owner.events = wrapper
	accepted := []contract.AcceptedPosition{{LineNo: 3, DishID: "d", Portions: 1}}
	if _, err := owner.CommitAcceptedOrder(ctx, contract.CommitOrderRequest{CommandID: "original-command", ProposalID: "same-proposal", AcceptedPositions: accepted}); !errors.Is(err, domain.ErrUnknownWriteOutcome) {
		t.Fatalf("initial pending commit error = %v; want UnknownWriteOutcome", err)
	}
	if owner.pending == nil {
		t.Fatal("unknown write did not retain the original command")
	}
	wantOrder := toContractOrder(*owner.pending.Events[0].Payload.Order)
	newIDs := 0
	owner.newID = func() string {
		newIDs++
		return "unexpected-id"
	}
	versionBeforeResolution, err := store.Version(ctx)
	if err != nil {
		t.Fatal(err)
	}
	result, err := owner.CommitAcceptedOrder(ctx, contract.CommitOrderRequest{CommandID: "retry-command", ProposalID: "same-proposal", AcceptedPositions: accepted})
	if err != nil || result.Kind != contract.CommitResultOrderCommitted || result.Order == nil || !reflect.DeepEqual(*result.Order, wantOrder) {
		t.Fatalf("new command for pending proposal = %#v, %v; want original order %#v", result, err, wantOrder)
	}
	versionAfterResolution, err := store.Version(ctx)
	if err != nil || versionAfterResolution != versionBeforeResolution+1 {
		t.Fatalf("version after resolving pending order = %d, %v; want one original order event", versionAfterResolution, err)
	}
	if newIDs != 0 {
		t.Fatalf("generated %d IDs while returning the original order; want none", newIDs)
	}
}

func TestOrderDiscoveredDuringPreparationReturnsOriginal(t *testing.T) {
	ctx := context.Background()
	now := moscowTime(2026, time.April, 2, 10)
	owner, store := newOrderCommitFixture(t, now, 80000)
	stateVersion := owner.state.StateVersion
	batch := externalOrderBatch(owner, "external-command", "same-proposal", 3, 1, "external-order", "external-event", 40000)
	wrapper := &appendOrderDuringVersionRead{EventStore: store, batch: batch}
	owner.events = wrapper
	newIDs := 0
	owner.newID = func() string {
		newIDs++
		return "unexpected-id"
	}
	wantOrder := toContractOrder(*batch.Events[0].Payload.Order)
	result, err := owner.CommitAcceptedOrder(ctx, contract.CommitOrderRequest{CommandID: "retry-command", ProposalID: "same-proposal", AcceptedPositions: []contract.AcceptedPosition{{LineNo: 3, DishID: "d", Portions: 1}}})
	if err != nil || result.Kind != contract.CommitResultOrderCommitted || result.Order == nil || !reflect.DeepEqual(*result.Order, wantOrder) {
		t.Fatalf("order discovered during preparation = %#v, %v; want original order %#v", result, err, wantOrder)
	}
	version, err := store.Version(ctx)
	if err != nil || version != stateVersion+1 {
		t.Fatalf("version after concurrent order discovery = %d, %v; want one external event", version, err)
	}
	if newIDs != 0 {
		t.Fatalf("generated %d IDs while returning the discovered order; want none", newIDs)
	}
}

func TestPendingRecordedOrderReturnsWhenProjectionApplyFails(t *testing.T) {
	ctx := context.Background()
	now := moscowTime(2026, time.April, 2, 10)
	owner, store := newOrderCommitFixture(t, now, 40000)
	batch := externalOrderBatch(owner, "original-command", "same-proposal", 3, 1, "original-order", "original-event", 40000)
	appendResult, err := store.AppendBatch(ctx, batch)
	if err != nil {
		t.Fatal(err)
	}
	versionBeforeRetry, err := store.Version(ctx)
	if err != nil {
		t.Fatal(err)
	}
	owner.pending = cloneCommandBatch(batch)
	wrapper := &hiddenProposalCorruptReadStore{EventStore: store}
	owner.events = wrapper
	newIDs := 0
	owner.newID = func() string {
		newIDs++
		return "unexpected-id"
	}
	wantOrder := toContractOrder(*appendResult.Command.Result.Order)
	result, err := owner.CommitAcceptedOrder(ctx, contract.CommitOrderRequest{CommandID: "retry-command", ProposalID: "same-proposal", AcceptedPositions: []contract.AcceptedPosition{{LineNo: 3, DishID: "d", Portions: 1}}})
	if err != nil || result.Kind != contract.CommitResultOrderCommitted || result.Order == nil || !reflect.DeepEqual(*result.Order, wantOrder) {
		t.Fatalf("pending order with failed projection apply = %#v, %v; want recorded order %#v", result, err, wantOrder)
	}
	if newIDs != 0 {
		t.Fatalf("generated %d IDs while returning the recorded order; want none", newIDs)
	}
	if !wrapper.corruptedRead {
		t.Fatal("projection read was not fault-injected")
	}
	versionAfterRetry, err := store.Version(ctx)
	if err != nil || versionAfterRetry != versionBeforeRetry {
		t.Fatalf("version after returning known order = %d, %v; want unchanged %d", versionAfterRetry, err, versionBeforeRetry)
	}
}

func TestCommitIdentityConflictDuringFailedPreparationPrecedesProposalOrder(t *testing.T) {
	ctx := context.Background()
	now := moscowTime(2026, time.April, 2, 10)
	owner, store := newOrderCommitFixture(t, now, 120000)
	stateVersion := owner.state.StateVersion
	foreignCommand := externalOrderBatch(owner, "retry-command", "foreign-proposal", 3, 1, "foreign-order", "foreign-event", 40000)
	proposalOrder := externalOrderBatch(owner, "other-command", "requested-proposal", 4, 1, "requested-order", "requested-event", 40000)
	proposalOrder.ExpectedVersion++
	wrapper := &commitCollisionDuringReadFailureStore{EventStore: store, firstVersion: stateVersion, batches: []domain.CommandBatch{foreignCommand, proposalOrder}}
	owner.events = wrapper
	newIDs := 0
	owner.newID = func() string {
		newIDs++
		return "unexpected-id"
	}
	result, err := owner.CommitAcceptedOrder(ctx, contract.CommitOrderRequest{CommandID: "retry-command", ProposalID: "requested-proposal", AcceptedPositions: []contract.AcceptedPosition{{LineNo: 3, DishID: "d", Portions: 1}}})
	if !errors.Is(err, contract.ErrCommandIdentityConflict) {
		t.Fatalf("CommitAcceptedOrder() = %#v, %v; want CommandIdentityConflict", result, err)
	}
	if !wrapper.readFailed {
		t.Fatal("preparation did not fail after recording the conflicting command")
	}
	if newIDs != 0 {
		t.Fatalf("generated %d IDs after command identity conflict; want none", newIDs)
	}
	version, err := store.Version(ctx)
	if err != nil || version != stateVersion+2 {
		t.Fatalf("version after collision = %d, %v; want only the two concurrent orders", version, err)
	}
	order, found, err := store.FindOrderByProposal(ctx, "requested-proposal")
	if err != nil || !found || order.OrderID != "requested-order" {
		t.Fatalf("concurrent proposal order = %#v, %v, %v; want recorded fallback candidate", order, found, err)
	}
}

type commitCollisionDuringReadFailureStore struct {
	EventStore
	firstVersion int64
	batches      []domain.CommandBatch
	injected     bool
	readFailed   bool
}

func (s *commitCollisionDuringReadFailureStore) Version(ctx context.Context) (int64, error) {
	if !s.injected {
		s.injected = true
		for _, batch := range s.batches {
			if _, err := s.AppendBatch(ctx, batch); err != nil {
				return 0, err
			}
		}
	}
	return s.EventStore.Version(ctx)
}

func (s *commitCollisionDuringReadFailureStore) ReadBatches(ctx context.Context, afterSequence int64) ([]domain.RecordedBatch, error) {
	if s.injected && afterSequence == s.firstVersion {
		s.readFailed = true
		return nil, errors.New("projection synchronization failed")
	}
	return s.EventStore.ReadBatches(ctx, afterSequence)
}

type delayConfirmOrderEventStore struct {
	EventStore
	unknownAttempts int
	confirmAttempts int
}

func (s *delayConfirmOrderEventStore) AppendBatch(ctx context.Context, batch domain.CommandBatch) (domain.AppendResult, error) {
	if batch.CommandType == "ConfirmOrder" {
		s.confirmAttempts++
		if s.confirmAttempts <= s.unknownAttempts {
			return domain.AppendResult{}, domain.ErrUnknownWriteOutcome
		}
	}
	return s.EventStore.AppendBatch(ctx, batch)
}

type appendOrderDuringVersionRead struct {
	EventStore
	batch  domain.CommandBatch
	insert bool
}

func (s *appendOrderDuringVersionRead) Version(ctx context.Context) (int64, error) {
	if !s.insert {
		s.insert = true
		if _, err := s.AppendBatch(ctx, s.batch); err != nil {
			return 0, err
		}
	}
	return s.EventStore.Version(ctx)
}

type hiddenProposalCorruptReadStore struct {
	EventStore
	hidProposal   bool
	corruptedRead bool
}

func (s *hiddenProposalCorruptReadStore) FindOrderByProposal(ctx context.Context, proposalID string) (domain.Order, bool, error) {
	if !s.hidProposal {
		s.hidProposal = true
		return domain.Order{}, false, nil
	}
	return s.EventStore.FindOrderByProposal(ctx, proposalID)
}

func (s *hiddenProposalCorruptReadStore) ReadBatches(ctx context.Context, afterSequence int64) ([]domain.RecordedBatch, error) {
	batches, err := s.EventStore.ReadBatches(ctx, afterSequence)
	if err == nil && !s.corruptedRead && len(batches) > 0 && batches[0].CommandType == "ConfirmOrder" {
		batches[0] = domain.CloneRecordedBatch(batches[0])
		batches[0].Events[0].Payload.Order.OrderID = ""
		s.corruptedRead = true
	}
	return batches, err
}

func newOrderCommitFixture(t *testing.T, now time.Time, stock contract.Quantity) (*Owner, EventStore) {
	t.Helper()
	ctx := context.Background()
	owner, store, _ := newCatalogOwner(t, now)
	if _, err := owner.RegisterProduct(ctx, contract.RegisterProductRequest{CommandID: "product-p", ProductID: "p", Name: "Pear", BaseUnit: contract.BaseUnitG}); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.DefineDish(ctx, contract.DefineDishRequest{CommandID: "dish-d", DishID: "d", Name: "Dish", Ingredients: []contract.Ingredient{{ProductID: "p", Quantity: 40000}}}); err != nil {
		t.Fatal(err)
	}
	delivery := validDeliveryRequest("delivery-p", "delivery-1", now)
	delivery.Lines[0].Quantity = stock
	if _, err := owner.ReceiveDelivery(ctx, delivery); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.Prepare(ctx); err != nil {
		t.Fatal(err)
	}
	return owner, store
}

func externalOrderBatch(owner *Owner, commandID, proposalID string, lineNo, portions int64, orderID, eventID string, quantity domain.Quantity) domain.CommandBatch {
	batchID := ""
	for id, batch := range owner.state.Batches {
		if batch.ProductID == "p" {
			batchID = id
			break
		}
	}
	order := domain.Order{OrderID: orderID, ProposalID: proposalID, AcceptedPositions: []domain.AcceptedPosition{{LineNo: lineNo, DishID: "d", Portions: portions}}, Allocations: []domain.Allocation{{ProductID: "p", BatchID: batchID, Quantity: quantity}}, PlacedAt: owner.state.Batches[batchID].ReceivedAt}
	event := domain.Event{EventID: eventID, RecordedAt: owner.state.Batches[batchID].ReceivedAt, Type: domain.EventOrderPlaced, Payload: domain.EventPayload{Order: &order}}
	return domain.CommandBatch{ExpectedVersion: owner.state.StateVersion, CommandID: commandID, CommandType: "ConfirmOrder", CommandFingerprint: canonicalConfirmOrderFP(proposalID), Events: []domain.Event{event}}
}
