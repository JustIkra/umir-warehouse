package application

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/JustIkra/umir-warehouse/contract"
	"github.com/JustIkra/umir-warehouse/internal/adapters/memory"
	"github.com/JustIkra/umir-warehouse/internal/adapters/zonedata"
	"github.com/JustIkra/umir-warehouse/internal/domain"
)

func TestW5PackagedMovementPeriodsIgnoreHostZone(t *testing.T) {
	if os.Getenv("UMIR_W5_ZONE_PROBE") != "1" {
		host := w5ConflictingZoneArchive(t)
		t.Setenv("ZONEINFO", host)
		t.Setenv("UMIR_W5_ZONE_PROBE", "1")
		binary, err := os.Executable()
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		output, err := exec.CommandContext(ctx, binary, "-test.run=^TestW5PackagedMovementPeriodsIgnoreHostZone$", "-test.v").CombinedOutput()
		t.Log(string(output))
		if err != nil {
			t.Fatal(err)
		}
		return
	}
	host, err := time.LoadLocation("Europe/Berlin")
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, time.March, 29, 22, 30, 0, 0, time.UTC)
	if _, offset := at.In(host).Zone(); offset != 0 {
		t.Fatal("host archive did not override Berlin with UTC")
	}
	eventAt := time.Date(2026, time.March, 28, 23, 30, 0, 0, time.UTC)
	clock := &testClock{now: eventAt}
	owner := w3Owner(t, memory.NewEventStore(), clock)
	if err = owner.ConfigureZoneLoader(zonedata.Load); err != nil {
		t.Fatal(err)
	}
	if err = owner.ConfigureTZData(zonedata.Version); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, err = owner.RegisterProduct(ctx, contract.RegisterProductRequest{CommandID: "probe-product", ProductID: "p", Name: "P", BaseUnit: contract.BaseUnitG}); err != nil {
		t.Fatal(err)
	}
	supplied := domain.CanonicalBusinessTime(eventAt)
	if _, err = owner.ReceiveDelivery(ctx, contract.ReceiveDeliveryRequest{CommandID: "probe-delivery", DeliveryID: "d", Number: "1", CreatedAt: supplied, Lines: []contract.DeliveryLine{{ProductID: "p", Quantity: 7, ManufacturedAt: supplied, ReceivedAt: supplied, ShelfLife: contract.ShelfLife{Value: 5, Unit: contract.ShelfLifeDay}}}}); err != nil {
		t.Fatal(err)
	}
	clock.Set(at)
	n := 0
	analytics, err := NewAnalytics(ownerStock{owner}, owner, eventAt, clock.Now, func() string { n++; return fmt.Sprintf("zone-cursor-%d", n) }, zonedata.Load)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, from, through string
		start, end          time.Time
	}{
		{"explicit DST day", "2026-03-29", "2026-03-29", time.Date(2026, time.March, 28, 23, 0, 0, 0, time.UTC), time.Date(2026, time.March, 29, 22, 0, 0, 0, time.UTC)},
		{"default crosses local date", "", "", time.Date(2026, time.March, 23, 23, 0, 0, 0, time.UTC), time.Date(2026, time.March, 30, 22, 0, 0, 0, time.UTC)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			request := w5Request()
			request.Scope.TimeZone = "Europe/Berlin"
			request.FromDate, request.ThroughDate = tc.from, tc.through
			report, err := analytics.Movements(ctx, request)
			if err != nil {
				t.Fatal(err)
			}
			if !report.From.Equal(tc.start) || !report.To.Equal(tc.end) {
				t.Errorf("period %s/%s (%s), want %s/%s (%s)", report.From, report.To, report.To.Sub(report.From), tc.start, tc.end, tc.end.Sub(tc.start))
			}
			if len(report.Items) != 1 || report.Items[0].ProductID != "p" || report.Items[0].ReceivedQuantity != 7 {
				t.Errorf("real seven-unit supply omitted: %+v", report.Items)
			}
		})
	}
}

func w5ConflictingZoneArchive(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile("../adapters/zonedata/zoneinfo.zip")
	if err != nil {
		t.Fatal(err)
	}
	archive, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	entry, err := archive.Open("UTC")
	if err != nil {
		t.Fatal(err)
	}
	utc, readErr := io.ReadAll(entry)
	if err = errors.Join(readErr, entry.Close()); err != nil {
		t.Fatal(err)
	}
	var buffer bytes.Buffer
	writer := zip.NewWriter(&buffer)
	file, err := writer.CreateHeader(&zip.FileHeader{Name: "Europe/Berlin", Method: zip.Store})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = file.Write(utc); err != nil {
		t.Fatal(err)
	}
	if err = writer.Close(); err != nil {
		t.Fatal(err)
	}
	host := filepath.Join(t.TempDir(), "host-zoneinfo.zip")
	if err = os.WriteFile(host, buffer.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	return host
}

func TestW5RecoveryReconcilesConcurrentJournalAdvance(t *testing.T) {
	ctx := context.Background()
	base := memory.NewEventStore()
	clock := &testClock{now: moscowTime(2026, time.October, 13, 12)}
	writer := w3Owner(t, base, clock)
	advanced := false
	store := &eventStoreWrapper{EventStore: base, read: func(_ int64, batches []domain.RecordedBatch, err error) ([]domain.RecordedBatch, error) {
		if err == nil && !advanced {
			advanced = true
			if _, err := writer.RegisterProduct(ctx, contract.RegisterProductRequest{CommandID: "concurrent", ProductID: "p", Name: "P", BaseUnit: contract.BaseUnitG}); err != nil {
				t.Fatal(err)
			}
		}
		return batches, err
	}}
	reader := w3Owner(t, store, clock)
	products, err := reader.GetProducts(ctx)
	if err != nil || products.StateVersion != 1 || len(products.Items) != 1 || products.Items[0].ProductID != "p" {
		t.Fatalf("concurrent committed product was treated as corruption: %+v %v", products, err)
	}
}

func TestW5RecoveryBoundsContentionAndRejectsCorruption(t *testing.T) {
	ctx := context.Background()
	clock := &testClock{now: moscowTime(2026, time.October, 13, 12)}
	base := memory.NewEventStore()
	writer := w3Owner(t, base, clock)
	advancing := true
	writes := 0
	store := &eventStoreWrapper{EventStore: base, read: func(_ int64, batches []domain.RecordedBatch, err error) ([]domain.RecordedBatch, error) {
		if err == nil && advancing {
			writes++
			id := fmt.Sprintf("advance-%d", writes)
			if _, err := writer.RegisterProduct(ctx, contract.RegisterProductRequest{CommandID: id, ProductID: id, Name: id, BaseUnit: contract.BaseUnitG}); err != nil {
				t.Fatal(err)
			}
		}
		return batches, err
	}}
	reader := w3Owner(t, store, clock)
	if _, err := reader.GetProducts(ctx); !errors.Is(err, contract.ErrInventoryUnavailable) || !errors.Is(err, domain.ErrConcurrentModification) || reader.usable || writes > 3 {
		t.Fatalf("continuous contention did not fail closed within three reads: writes=%d usable=%t err=%v", writes, reader.usable, err)
	}
	advancing = false
	products, err := reader.GetProducts(ctx)
	if err != nil || products.StateVersion != int64(writes) || len(products.Items) != writes {
		t.Fatalf("quiescent recovery lost committed facts: %+v %v", products, err)
	}
	corruptReads := 0
	corrupt := &eventStoreWrapper{EventStore: base, read: func(_ int64, batches []domain.RecordedBatch, err error) ([]domain.RecordedBatch, error) {
		corruptReads++
		if err == nil && len(batches) > 0 {
			batches[0].Events[0].Sequence++
		}
		return batches, err
	}}
	reader = w3Owner(t, corrupt, clock)
	if _, err := reader.GetProducts(ctx); !errors.Is(err, domain.ErrInvalidRecordedBatch) || reader.usable || corruptReads != 1 {
		t.Fatalf("corruption was retried or published: reads=%d usable=%t err=%v", corruptReads, reader.usable, err)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := reader.GetProducts(canceled); !errors.Is(err, context.Canceled) || reader.usable {
		t.Fatalf("canceled recovery did not fail closed: usable=%t err=%v", reader.usable, err)
	}
}
