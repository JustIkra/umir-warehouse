package integration_test

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"

	"github.com/JustIkra/umir-warehouse/internal/application"
	"github.com/JustIkra/umir-warehouse/internal/domain"
)

const commitProposal1 = `{"commandID":"c1","proposalID":"P1","acceptedPositions":[{"lineNo":"1","dishID":"D","portions":"1"}]}`

type commitWire struct {
	Context struct {
		EvaluatedAt  string `json:"evaluatedAt"`
		StateVersion string `json:"stateVersion"`
	} `json:"context"`
	Result struct {
		Order contractOrder `json:"order"`
	} `json:"result"`
}

func decodeCommit(t *testing.T, body string) commitWire {
	t.Helper()
	var wire commitWire
	if err := json.Unmarshal([]byte(body), &wire); err != nil {
		t.Fatalf("decode %q: %v", body, err)
	}
	return wire
}

// A second command for a proposal that is already an order answers with that
// order and the position where it was placed, whatever command placed it.
func TestSameProposalNewCommandOverHTTP(t *testing.T) {
	f, _ := w6Stock(t)
	first := w6Call(t, f, tenantA, "POST", "order-commits", "confirmOrder", "consumer.order.commit", commitProposal1)
	if first.Code != 200 {
		t.Fatalf("first commit: %d %s", first.Code, first.Body)
	}
	second := w6Call(t, f, tenantA, "POST", "order-commits", "confirmOrder", "consumer.order.commit", strings.Replace(commitProposal1, `"c1"`, `"c2"`, 1))
	if second.Code != 200 {
		t.Fatalf("second commit for the same proposal: %d %s", second.Code, second.Body)
	}
	one, two := decodeCommit(t, first.Body.String()), decodeCommit(t, second.Body.String())
	if one.Result.Order.ID == "" || one.Result.Order.ID != two.Result.Order.ID {
		t.Fatalf("order IDs %q and %q; want the same order", one.Result.Order.ID, two.Result.Order.ID)
	}
	if one.Context.EvaluatedAt != two.Context.EvaluatedAt || one.Context.StateVersion != two.Context.StateVersion {
		t.Fatalf("contexts differ: %+v and %+v; want the position of the placement", one.Context, two.Context)
	}
	attempt := w6Call(t, f, tenantA, "GET", "order-attempts?commandID=c2", "resolveOrder", "consumer.order.resolve", "")
	if attempt.Code != 200 || !strings.Contains(attempt.Body.String(), `"state":"Completed"`) {
		t.Fatalf("attempt c2: %d %s", attempt.Code, attempt.Body)
	}
	retry := w6Call(t, f, tenantA, "POST", "order-commits", "confirmOrder", "consumer.order.commit", strings.Replace(commitProposal1, `"c1"`, `"c2"`, 1))
	if retry.Code != 200 || retry.Body.String() != second.Body.String() {
		t.Fatalf("retry of c2: %d %s; want the second answer %s", retry.Code, retry.Body, second.Body)
	}
}

// journalReads counts the reads of the journal from its start.
type journalReads struct {
	application.EventStore
	mu    sync.Mutex
	count int
}

func (s *journalReads) ReadBatches(ctx context.Context, after int64) ([]domain.RecordedBatch, error) {
	s.note(after)
	return s.EventStore.ReadBatches(ctx, after)
}

func (s *journalReads) ScanBatches(ctx context.Context, after int64, fn func(domain.RecordedBatch) error) error {
	s.note(after)
	return s.EventStore.ScanBatches(ctx, after, fn)
}

func (s *journalReads) note(after int64) {
	if after == 0 {
		s.mu.Lock()
		s.count++
		s.mu.Unlock()
	}
}

func (s *journalReads) reads() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.count
}

// A commit and the passive reads around it cost the same however long the
// journal is, so none of them reads it from the start.
func TestCommitDoesNotReadJournalOverHTTP(t *testing.T) {
	var stores []*journalReads
	f := setupWith(t, func(inner application.EventStore) application.EventStore {
		store := &journalReads{EventStore: inner}
		stores = append(stores, store)
		return store
	})
	runtime := w6Seed(t, f)
	if err := runtime.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	reads := func() int {
		total := 0
		for _, store := range stores {
			total += store.reads()
		}
		return total
	}
	before := reads()
	first := w6Call(t, f, tenantA, "POST", "order-commits", "confirmOrder", "consumer.order.commit", commitProposal1)
	if first.Code != 200 {
		t.Fatalf("commit: %d %s", first.Code, first.Body)
	}
	orderID := decodeCommit(t, first.Body.String()).Result.Order.ID
	for _, request := range []struct{ method, path, op, capability, body string }{
		{"POST", "order-commits", "confirmOrder", "consumer.order.commit", strings.Replace(commitProposal1, `"c1"`, `"c2"`, 1)},
		{"GET", "orders?orderID=" + orderID, "resolveOrder", "consumer.order.resolve", ""},
		{"GET", "order-attempts?commandID=c1", "resolveOrder", "consumer.order.resolve", ""},
		{"GET", "commands?commandID=c1", "resolveOrder", "consumer.order.resolve", ""},
		{"GET", "history?fromSequence=2", "history", "stock.read", ""},
	} {
		if w := w6Call(t, f, tenantA, request.method, request.path, request.op, request.capability, request.body); w.Code != 200 {
			t.Fatalf("%s %s: %d %s", request.method, request.path, w.Code, w.Body)
		}
	}
	if got := reads() - before; got != 0 {
		t.Fatalf("commit and passive reads read the journal from the start %d times; want 0", got)
	}
}
