package tests

import (
	"context"
	"testing"
	"time"

	"github.com/JustIkra/umir-warehouse/internal/adapters/memory"
	"github.com/JustIkra/umir-warehouse/internal/application"
	"github.com/JustIkra/umir-warehouse/internal/domain"
)

func TestFingerprintConfirmOrderIdentity(t *testing.T) {
	got := domain.Fingerprint("ConfirmOrder", []byte(`{"proposalID":"p1"}`))
	const want = "67610c5d5f3bb1111e064b8ca1f6c8e020c17f3d3dd9651ebb3a3fc69d9ac3b5"
	if got != want {
		t.Fatalf("Fingerprint() = %q; want %q", got, want)
	}
}

func TestEventStorePortPreservesCommandBatch(t *testing.T) {
	var store application.EventStore = memory.NewEventStore()
	ctx := context.Background()
	batch := domain.CommandBatch{
		CommandID:          "delivery-command",
		CommandType:        "ReceiveDelivery",
		CommandFingerprint: "receive-fingerprint",
		Events: []domain.Event{
			{EventID: "delivery-received", RecordedAt: testMoscowTime(2026, 3, 1), Type: domain.EventDeliveryReceived, Payload: domain.EventPayload{
				Delivery: &domain.Delivery{DeliveryID: "delivery-1", Number: "invoice-1", Lines: []domain.DeliveryLine{{ProductID: "p1", Quantity: 1000}}},
				Batches:  []domain.Batch{{BatchID: "batch-1", DeliveryID: "delivery-1", ProductID: "p1", ExpiresAt: testMoscowTime(2026, 3, 1), ReceivedQuantity: 1000, RemainingQuantity: 1000}},
			}},
			{EventID: "batch-expired", RecordedAt: testMoscowTime(2026, 3, 1), Type: domain.EventBatchExpired, Payload: domain.EventPayload{Expiry: &domain.ExpiryResult{BatchID: "batch-1", ProductID: "p1", WrittenOffQuantity: 1000, Reason: domain.ExpiryReasonExpiration, ExpiresAt: testMoscowTime(2026, 3, 1), ProcessedAt: testMoscowTime(2026, 3, 1)}}},
		},
	}
	result, err := store.AppendBatch(ctx, batch)
	if err != nil {
		t.Fatal(err)
	}
	if result.Batch.FinalSequence != 2 || len(result.Batch.Events) != 2 {
		t.Fatalf("AppendBatch() = %#v; want both events in one complete recorded batch", result.Batch)
	}
	command, ok, err := store.FindCommand(ctx, batch.CommandID)
	if err != nil || !ok {
		t.Fatalf("FindCommand() = %#v, %v, %v; want recorded receive command", command, ok, err)
	}
	if command.CommandType != "ReceiveDelivery" || command.Result.Kind != domain.CommandResultDeliveryReceived || command.Result.Delivery == nil {
		t.Fatalf("recorded delivery result = %#v; want DeliveryReceived receipt", command.Result)
	}
	if len(command.Result.Delivery.BatchIDs) != 1 || command.Result.Delivery.BatchIDs[0] != "batch-1" {
		t.Fatalf("recorded receipt = %#v; want batch-1", command.Result.Delivery)
	}
}

func testMoscowTime(year int, month time.Month, day int) time.Time {
	return time.Date(year, month, day, 12, 0, 0, 0, time.FixedZone("MSK", 3*60*60))
}
