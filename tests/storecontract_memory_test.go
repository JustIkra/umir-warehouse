package tests

import (
	"testing"

	"github.com/JustIkra/umir-warehouse/internal/adapters/memory"
	"github.com/JustIkra/umir-warehouse/tests/storecontract"
)

func memoryFixture() storecontract.Fixture {
	newStores := func(*testing.T) storecontract.Stores {
		return storecontract.Stores{Events: memory.NewEventStore(), Snapshots: memory.NewSnapshotStore()}
	}
	return storecontract.Fixture{
		New:   newStores,
		Other: func(t *testing.T, _ storecontract.Stores) storecontract.Stores { return newStores(t) },
	}
}

func TestMemoryEventStoreContract(t *testing.T) {
	storecontract.RunEventStore(t, memoryFixture())
}

func TestMemorySnapshotStoreContract(t *testing.T) {
	storecontract.RunSnapshotStore(t, memoryFixture())
}
