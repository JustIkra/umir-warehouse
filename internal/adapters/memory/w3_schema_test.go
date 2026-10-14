package memory

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/JustIkra/umir-warehouse/internal/domain"
)

func TestW3AdmissionRejectsMismatchedEventSchema(t *testing.T) {
	at := time.Date(2026, 10, 13, 9, 0, 0, 0, time.FixedZone("MSK", 10800))
	for _, test := range []struct {
		name, command string
		event         domain.Event
	}{{"old-type-with-v2", "RegisterProduct", domain.Event{EventID: "old", RecordedAt: at, Type: domain.EventProductRegistered, SchemaVersion: 2, Payload: domain.EventPayload{Product: &domain.Product{ProductID: "P", Name: "Synthetic", BaseUnit: domain.BaseUnitG}}}}, {"pending-type-with-v1", "PrepareExpiry", domain.Event{EventID: "pending", TenantID: "33333333-3333-4333-8333-333333333333", RuntimeEpoch: "44444444-4444-4444-8444-444444444444", RecordedAt: at, Type: domain.EventBatchExpiryPending, SchemaVersion: 1, Payload: domain.EventPayload{Pending: &domain.PendingResult{BatchID: "B", ProductID: "P", RemainingQuantity: 1, BaseUnit: domain.BaseUnitG, ExpiresAt: at}}}}} {
		t.Run(test.name, func(t *testing.T) {
			store := NewEventStore()
			_, err := store.AppendBatch(context.Background(), domain.CommandBatch{CommandID: "command", CommandType: test.command, CommandFingerprint: "fingerprint", Events: []domain.Event{test.event}})
			if !errors.Is(err, domain.ErrUnsupportedSchema) {
				t.Fatalf("schema mismatch admitted: %v", err)
			}
			version, err := store.Version(context.Background())
			if err != nil || version != 0 {
				t.Fatal("refusal changed event store")
			}
		})
	}
}
