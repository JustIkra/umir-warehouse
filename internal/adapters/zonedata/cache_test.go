package zonedata

import (
	"sync"
	"testing"

	"github.com/JustIkra/umir-warehouse/contract"
)

func TestRepeatedLoadAllocatesAtMostOnce(t *testing.T) {
	if _, err := Load("Europe/Moscow"); err != nil {
		t.Fatal(err)
	}
	allocs := testing.AllocsPerRun(100, func() {
		if _, err := Load("Europe/Moscow"); err != nil {
			t.Fatal(err)
		}
	})
	if allocs > 1 {
		t.Fatalf("a repeated Load allocates %.0f times; want at most 1", allocs)
	}
}

func TestLoadIsSafeForConcurrentUse(t *testing.T) {
	names := []string{"Europe/Moscow", "Asia/Yekaterinburg", "Europe/Samara", "UTC"}
	var wg sync.WaitGroup
	for i := range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 50 {
				zone, err := Load(names[i%len(names)])
				if err != nil || zone.String() != names[i%len(names)] {
					t.Errorf("Load(%q) = %v, %v", names[i%len(names)], zone, err)
					return
				}
			}
		}()
	}
	wg.Wait()
}

func TestLoadDoesNotCacheFailures(t *testing.T) {
	for range 3 {
		if _, err := Load("Not/AZone"); err != contract.ErrUnknownTimeZone {
			t.Fatalf("Load(unknown) = %v; want ErrUnknownTimeZone", err)
		}
	}
}

func BenchmarkLoadRepeated(b *testing.B) {
	b.ReportAllocs()
	for range b.N {
		if _, err := Load("Europe/Moscow"); err != nil {
			b.Fatal(err)
		}
	}
}
