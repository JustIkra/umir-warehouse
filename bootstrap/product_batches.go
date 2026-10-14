package bootstrap

import (
	"context"

	"github.com/JustIkra/umir-warehouse/contract"
)

func (r *Runtime) ProductBatches(ctx context.Context, request contract.ProductBatchesRequest) (contract.ProductBatchesPage, error) {
	if err := r.enter(ctx); err != nil {
		return contract.ProductBatchesPage{}, err
	}
	defer r.leave()
	if err := r.owner.ValidateAnalyticsScope(request.Scope); err != nil {
		if request.Cursor != "" {
			err = contract.ErrRefreshRequired
		}
		return contract.ProductBatchesPage{}, err
	}
	if r.productBatches == nil {
		return contract.ProductBatchesPage{}, contract.ErrRuntimeNotReady
	}
	return r.productBatches.Read(ctx, request)
}
