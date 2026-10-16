package warehousepg

import (
	"testing"

	"github.com/JustIkra/umir-warehouse/internal/domain"
)

func TestValidCommandBatchRejectsControlCharactersInCorrelationID(t *testing.T) {
	batch := domain.CommandBatch{CommandID: "command-1", CommandType: "RegisterProduct", CommandFingerprint: "fingerprint", Events: []domain.Event{{EventID: "event-1"}}}
	if !validCommandBatch(batch) {
		t.Fatal("valid command batch was rejected")
	}
	batch.CommandID = "command\n1"
	if validCommandBatch(batch) {
		t.Fatal("command ID with a control character was accepted")
	}
}
