package warehousepg

import (
	"context"
	"strconv"

	"github.com/JustIkra/umir-warehouse/contract"
	"github.com/JustIkra/umir-warehouse/internal/adapters/postgres"
	"github.com/JustIkra/umir-warehouse/internal/application"
)

type Stores struct {
	Events    application.EventStore
	Snapshots application.SnapshotStore
}

func NewRestaurantStores(ctx context.Context, db *postgres.DB, scope contract.Scope) (Stores, error) {
	if db == nil || scope.Validate() != nil {
		return Stores{}, contract.ErrInvalidArgument
	}
	generation, err := postgres.Generation(ctx, db)
	if err != nil {
		return Stores{}, err
	}
	if generation != scope.RuntimeEpoch {
		return Stores{}, contract.ErrRuntimeReset
	}
	lifecycleVersion, err := strconv.ParseInt(scope.LifecycleVersion, 10, 64)
	if err != nil {
		return Stores{}, contract.ErrInvalidArgument
	}
	storageScope := postgres.Scope{
		Actor: postgres.ActorTenant, OrgID: scope.OrganizationID,
		RestaurantID: scope.RestaurantID,
	}
	return Stores{
		Events:    &EventStore{db: db, scope: storageScope, generation: generation, lifecycleVersion: lifecycleVersion},
		Snapshots: &SnapshotStore{db: db, scope: storageScope},
	}, nil
}
