package warehousepg

import (
	"testing"
	"time"

	"github.com/JustIkra/umir-warehouse/internal/domain"
)

func TestEventCodecPreservesBusinessInstantAndSchemaPayload(t *testing.T) {
	zone := time.FixedZone("MSK", 3*60*60)
	instant := time.Date(2026, time.October, 16, 10, 18, 48, 123456789, zone)
	event := domain.Event{
		TenantID: "restaurant", RuntimeEpoch: "generation", EventID: "event-1",
		StreamID: domain.InventoryStreamID, Sequence: 1, SchemaVersion: domain.InventorySchemaV2,
		CommandID: "command-1", CommandFingerprint: "fingerprint", RecordedAt: instant,
		Type: domain.EventBatchExpiryPending,
		Payload: domain.EventPayload{Pending: &domain.PendingResult{
			BatchID: "batch-1", ProductID: "product-1", RemainingQuantity: 1000,
			BaseUnit: domain.BaseUnitG, ExpiresAt: instant.Add(time.Hour),
		}},
	}
	encoded, err := encodeEvent(event)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := decodeEvent(encoded)
	if err != nil {
		t.Fatal(err)
	}
	if !decoded.RecordedAt.Equal(instant) || decoded.RecordedAt.Nanosecond() != instant.Nanosecond() {
		t.Fatalf("recordedAt = %s, want %s", decoded.RecordedAt, instant)
	}
	_, offset := decoded.RecordedAt.Zone()
	if offset != 3*60*60 || decoded.SchemaVersion != domain.InventorySchemaV2 || decoded.Payload.Pending == nil || decoded.Payload.Pending.ExpiresAt.Nanosecond() != instant.Nanosecond() {
		t.Fatalf("decoded event lost schema, zone, nanoseconds or payload: %#v", decoded)
	}
}

func TestSnapshotCodecChecksDigestAndRestoresDerivedBatchIndex(t *testing.T) {
	state := domain.InventoryState{
		Products: map[string]domain.Product{"p1": {ProductID: "p1", Name: "Product", BaseUnit: domain.BaseUnitG}},
		Dishes:   map[string]domain.Dish{}, DeliveryIDs: map[string]struct{}{},
		Batches: map[string]domain.Batch{}, StateVersion: 1,
	}
	encoded, digest, err := encodeSnapshotState(state)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := decodeSnapshotState(encoded, digest)
	if err != nil {
		t.Fatal(err)
	}
	if !decoded.Indexed() {
		t.Fatal("decoded state has no derived batch index")
	}
	if _, err := decodeSnapshotState(encoded, "0000000000000000000000000000000000000000000000000000000000000000"); err == nil {
		t.Fatal("snapshot codec accepted an incorrect checksum")
	}
}
