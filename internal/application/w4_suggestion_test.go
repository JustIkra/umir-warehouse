package application

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/JustIkra/umir-warehouse/contract"
	"github.com/JustIkra/umir-warehouse/internal/adapters/memory"
	"github.com/JustIkra/umir-warehouse/internal/domain"
)

type w4UnavailableStore struct {
	EventStore
	unavailable bool
}

func (s *w4UnavailableStore) Version(ctx context.Context) (int64, error) {
	if s.unavailable {
		return 0, errors.New("test store unavailable")
	}
	return s.EventStore.Version(ctx)
}
func TestW4SuggestionFailsClosedOnUnavailableProjection(t *testing.T) {
	ctx := context.Background()
	store := &w4UnavailableStore{EventStore: memory.NewEventStore()}
	owner, err := NewOwner(store, func() time.Time { return moscowTime(2026, time.October, 13, 12) }, rand.Text)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := owner.SuggestID(ctx, contract.CatalogProduct); err != nil || got.SuggestedID != "1" {
		t.Fatalf("healthy: %+v %v", got, err)
	}
	store.unavailable = true
	for _, kind := range []contract.CatalogKind{contract.CatalogProduct, contract.CatalogDish} {
		got, err := owner.SuggestID(ctx, kind)
		if !errors.Is(err, contract.ErrInventoryUnavailable) || got != (contract.IDSuggestion{}) {
			t.Fatalf("unavailable: %+v %v", got, err)
		}
	}
	store.unavailable = false
	if got, err := owner.SuggestID(ctx, contract.CatalogDish); err != nil || got.SuggestedID != "1" {
		t.Fatalf("recovered: %+v %v", got, err)
	}
	if got, err := owner.SuggestID(ctx, "Product"); !errors.Is(err, contract.ErrInvalidArgument) || got != (contract.IDSuggestion{}) {
		t.Fatalf("invalid kind: %+v %v", got, err)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if got, err := owner.SuggestID(canceled, contract.CatalogProduct); !errors.Is(err, context.Canceled) || got != (contract.IDSuggestion{}) {
		t.Fatalf("canceled: %+v %v", got, err)
	}
}

func TestW4SuggestionCapturesSharedCatalogAndDecimalCarry(t *testing.T) {
	ctx := context.Background()
	store := memory.NewEventStore()
	clock := func() time.Time { return moscowTime(2026, time.October, 13, 12) }
	reader, err := NewOwner(store, clock, rand.Text)
	if err != nil {
		t.Fatal(err)
	}
	writer, err := NewOwner(store, clock, rand.Text)
	if err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= 100; i++ {
		id := fmt.Sprint(i)
		_, err := writer.RegisterProduct(ctx, contract.RegisterProductRequest{CommandID: "p-" + id, ProductID: id, Name: "product", BaseUnit: contract.BaseUnitG})
		if err != nil {
			t.Fatal(err)
		}
	}
	got, err := reader.SuggestID(ctx, contract.CatalogProduct)
	if err != nil || got.SuggestedID != "101" || got.StateVersion != 100 || got.Reserved || !got.EvaluatedAt.Equal(clock()) {
		t.Fatalf("current prefix: %+v %v", got, err)
	}
	if got, err := reader.SuggestID(ctx, contract.CatalogDish); err != nil || got.SuggestedID != "1" || got.StateVersion != 100 {
		t.Fatalf("independent dish: %+v %v", got, err)
	}
}

func TestW4SuggestionLeavesDueExpiryUntouched(t *testing.T) {
	instant := moscowTime(2026, time.April, 2, 10)
	store := seedStore(t, instant, 10000, 0)
	owner, err := NewOwner(store, func() time.Time { return instant }, rand.Text)
	if err != nil {
		t.Fatal(err)
	}
	for _, kind := range []contract.CatalogKind{contract.CatalogProduct, contract.CatalogDish} {
		got, err := owner.SuggestID(context.Background(), kind)
		if err != nil || got.StateVersion != 3 || got.Reserved {
			t.Fatalf("suggestion: %+v %v", got, err)
		}
	}
	version, err := store.Version(context.Background())
	if err != nil || version != 3 || owner.state.Batches["batch"].RemainingQuantity != 10000 {
		t.Fatalf("due stock changed: version %d %v", version, err)
	}
	if _, due := owner.nextDueBatch(instant); !due {
		t.Fatal("fixture had no due expiry")
	}
}

func TestW4SharedStoreCreateConflictAndImmutableResults(t *testing.T) {
	for _, kind := range []contract.CatalogKind{contract.CatalogProduct, contract.CatalogDish} {
		t.Run(string(kind), func(t *testing.T) {
			ctx := context.Background()
			store := memory.NewEventStore()
			clock := &testClock{now: moscowTime(2026, time.October, 13, 12)}
			owners := [2]*Owner{}
			for i := range owners {
				var err error
				owners[i], err = NewOwner(store, clock.Now, rand.Text)
				if err != nil {
					t.Fatal(err)
				}
			}
			if _, err := owners[0].RegisterProduct(ctx, contract.RegisterProductRequest{CommandID: "ingredient", ProductID: "P", Name: "ingredient", BaseUnit: contract.BaseUnitG}); err != nil {
				t.Fatal(err)
			}
			create := func(o *Owner, id int) (any, error) {
				if kind == contract.CatalogProduct {
					return o.RegisterProduct(ctx, contract.RegisterProductRequest{CommandID: fmt.Sprintf("create-%d", id), ProductID: "1", Name: fmt.Sprint(id), BaseUnit: contract.BaseUnitG})
				}
				return o.DefineDish(ctx, contract.DefineDishRequest{CommandID: fmt.Sprintf("create-%d", id), DishID: "1", Name: fmt.Sprint(id), Ingredients: []contract.Ingredient{{ProductID: "P", Quantity: 1}}})
			}
			errs := [2]error{}
			results := [2]any{}
			start := make(chan struct{})
			var wg sync.WaitGroup
			for i := range owners {
				wg.Go(func() { <-start; results[i], errs[i] = create(owners[i], i) })
			}
			close(start)
			wg.Wait()
			winner := -1
			conflict := contract.ErrProductAlreadyExists
			if kind == contract.CatalogDish {
				conflict = contract.ErrDishAlreadyExists
			}
			for i, err := range errs {
				if err == nil {
					if winner != -1 {
						t.Fatal("two successes")
					}
					winner = i
				} else if !errors.Is(err, conflict) {
					t.Fatal(err)
				}
			}
			if winner == -1 {
				t.Fatal("no success")
			}
			clock.now = clock.now.Add(time.Hour)
			if _, err := owners[0].RegisterProduct(ctx, contract.RegisterProductRequest{CommandID: "manual", ProductID: "自由", Name: "manual", BaseUnit: contract.BaseUnitG}); err != nil {
				t.Fatal(err)
			}
			replay, err := create(owners[1-winner], winner)
			if err != nil || !reflect.DeepEqual(replay, results[winner]) {
				t.Fatalf("retained result changed: %#v vs %#v (%v)", replay, results[winner], err)
			}
			if version, err := store.Version(ctx); err != nil || version != 3 {
				t.Fatalf("replay appended: %d %v", version, err)
			}
		})
	}
}

func TestW4DecimalSuggestionsHaveNoFixedWidthBound(t *testing.T) {
	for _, pair := range [][2]string{{"9", "10"}, {"99", "100"}, {"9223372036854775807", "9223372036854775808"}, {"18446744073709551615", "18446744073709551616"}, {"9999999999999999999999999999999999999999", "10000000000000000000000000000000000000000"}} {
		if got := nextDecimalID(pair[0]); got != pair[1] {
			t.Fatalf("increment %s: %s, want %s", pair[0], got, pair[1])
		}
	}
}

func TestW4SuggestionPreservesPendingCreationUntilExactRetry(t *testing.T) {
	for _, kind := range []contract.CatalogKind{contract.CatalogProduct, contract.CatalogDish} {
		t.Run(string(kind), func(t *testing.T) {
			ctx := context.Background()
			base := memory.NewEventStore()
			store := &delayedCommitStore{EventStore: base, commandID: "pending-create"}
			owner := newOwnerForCatalogTest(t, store, moscowTime(2026, time.October, 13, 12))
			if kind == contract.CatalogDish {
				if _, err := owner.RegisterProduct(ctx, contract.RegisterProductRequest{CommandID: "ingredient", ProductID: "P", Name: "ingredient", BaseUnit: contract.BaseUnitG}); err != nil {
					t.Fatal(err)
				}
			}
			create := func() error {
				if kind == contract.CatalogProduct {
					_, err := owner.RegisterProduct(ctx, contract.RegisterProductRequest{CommandID: "pending-create", ProductID: "1", Name: "pending", BaseUnit: contract.BaseUnitG})
					return err
				}
				_, err := owner.DefineDish(ctx, contract.DefineDishRequest{CommandID: "pending-create", DishID: "1", Name: "pending", Ingredients: []contract.Ingredient{{ProductID: "P", Quantity: 1}}})
				return err
			}
			if err := create(); !errors.Is(err, domain.ErrUnknownWriteOutcome) {
				t.Fatalf("setup expected unknown: %v", err)
			}
			before, err := base.Version(ctx)
			if err != nil {
				t.Fatal(err)
			}
			pending := cloneCommandBatch(*owner.pending)
			state := domain.CloneInventoryState(owner.state)
			attempts := store.appendAttempts
			for range 2 {
				for _, queriedKind := range []contract.CatalogKind{contract.CatalogProduct, contract.CatalogDish} {
					got, err := owner.SuggestID(ctx, queriedKind)
					if !errors.Is(err, contract.ErrInventoryUnavailable) || got != (contract.IDSuggestion{}) {
						t.Errorf("unresolved suggestion: %+v %v", got, err)
					}
				}
			}
			after, err := base.Version(ctx)
			if err != nil {
				t.Fatal(err)
			}
			_, found, err := base.FindCommand(ctx, "pending-create")
			if err != nil {
				t.Fatal(err)
			}
			if after != before || store.appendAttempts != attempts || found || !reflect.DeepEqual(owner.pending, pending) || !reflect.DeepEqual(owner.state, state) {
				t.Fatalf("suggestion changed pending creation: version %d -> %d, attempts %d -> %d, admitted %v", before, after, attempts, store.appendAttempts, found)
			}
			if err := create(); err != nil {
				t.Fatalf("exact retry: %v", err)
			}
			got, err := owner.SuggestID(ctx, kind)
			if err != nil || got.SuggestedID != "2" || got.StateVersion != before+1 || got.Reserved {
				t.Fatalf("resolved suggestion: %+v %v", got, err)
			}
			if err := create(); err != nil || store.appendAttempts != attempts+1 {
				t.Fatalf("retained retry: %v, attempts %d", err, store.appendAttempts)
			}
		})
	}
}
