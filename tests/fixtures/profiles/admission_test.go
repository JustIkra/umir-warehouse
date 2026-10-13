package profiles

import (
	"crypto/rand"
	"errors"
	"testing"
	"time"

	"github.com/JustIkra/umir-warehouse/bootstrap"
	"github.com/JustIkra/umir-warehouse/contract"
	"github.com/JustIkra/umir-warehouse/internal/adapters/zonedata"
)

func TestActualPackagedConstructorAdmission(t *testing.T) {
	info, inspectErr := zonedata.Inspect()
	options := bootstrap.Options{Now: func() time.Time { return time.Date(2026, 10, 13, 9, 0, 0, 0, time.FixedZone("Europe/Moscow", 10800)) }, NewID: rand.Text, EventsPerSnapshot: 100}
	registry, registryErr := bootstrap.NewRegistry(options, "11111111-1111-4111-8111-111111111111")
	runtime, runtimeErr := bootstrap.NewInMemory(options)
	if inspectErr != nil {
		if info.Bytes != 0 || !errors.Is(inspectErr, contract.ErrTimeZoneDataUnavailable) || !errors.Is(registryErr, contract.ErrTimeZoneDataUnavailable) || !errors.Is(runtimeErr, contract.ErrTimeZoneDataUnavailable) || registry != nil || runtime != nil {
			t.Fatalf("actual refusal mismatch: %+v %v %v %v", info, inspectErr, registryErr, runtimeErr)
		}
	} else if registryErr != nil || runtimeErr != nil || registry == nil || runtime == nil {
		t.Fatalf("healthy composition: %v %v", registryErr, runtimeErr)
	}
}
