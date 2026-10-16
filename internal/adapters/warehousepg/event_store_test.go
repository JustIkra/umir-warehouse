package warehousepg

import (
	"testing"
	"time"

	"github.com/JustIkra/umir-warehouse/internal/domain"
)

func TestStoredEventMatchesRejectsIndexedPayloadMismatch(t *testing.T) {
	recordedAt := time.Date(2026, time.October, 16, 10, 0, 0, 123456789, time.FixedZone("MSK", 3*60*60))
	event := domain.Event{Sequence: 1, EventID: "event-1", Type: domain.EventProductRegistered,
		SchemaVersion: domain.InventorySchemaV1, CommandID: "command-1", RecordedAt: recordedAt}
	if !storedEventMatches(event, 1, "event-1", string(event.Type), 1, "command-1", recordedAt.Truncate(time.Microsecond)) {
		t.Fatal("matching sidecar was rejected")
	}
	for _, mismatch := range []struct {
		name       string
		seq        int64
		id         string
		typeName   string
		schema     int16
		command    string
		recordedAt time.Time
	}{
		{name: "sequence", seq: 2, id: "event-1", typeName: string(event.Type), schema: 1, command: "command-1", recordedAt: recordedAt},
		{name: "event ID", seq: 1, id: "event-2", typeName: string(event.Type), schema: 1, command: "command-1", recordedAt: recordedAt},
		{name: "type", seq: 1, id: "event-1", typeName: string(domain.EventDishDefined), schema: 1, command: "command-1", recordedAt: recordedAt},
		{name: "schema", seq: 1, id: "event-1", typeName: string(event.Type), schema: 2, command: "command-1", recordedAt: recordedAt},
		{name: "command", seq: 1, id: "event-1", typeName: string(event.Type), schema: 1, command: "command-2", recordedAt: recordedAt},
		{name: "recorded time", seq: 1, id: "event-1", typeName: string(event.Type), schema: 1, command: "command-1", recordedAt: recordedAt.Add(time.Second)},
	} {
		t.Run(mismatch.name, func(t *testing.T) {
			if storedEventMatches(event, mismatch.seq, mismatch.id, mismatch.typeName, mismatch.schema, mismatch.command, mismatch.recordedAt) {
				t.Fatal("inconsistent sidecar was accepted")
			}
		})
	}
}
