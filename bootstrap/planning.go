package bootstrap

import (
	"context"

	"github.com/JustIkra/umir-warehouse/contract"
)

func (r *Runtime) SupplySuggestions(ctx context.Context, request contract.PlanningRequest) (contract.SupplySuggestions, error) {
	if err := r.enter(ctx); err != nil {
		return contract.SupplySuggestions{}, err
	}
	defer r.leave()
	if r.planning == nil {
		return contract.SupplySuggestions{}, contract.ErrRuntimeNotReady
	}
	return r.planning.Supply(ctx, request)
}

func (r *Runtime) RecommendationInputs(ctx context.Context, request contract.PlanningRequest) (contract.RecommendationInputs, error) {
	if err := r.enter(ctx); err != nil {
		return contract.RecommendationInputs{}, err
	}
	defer r.leave()
	if r.planning == nil {
		return contract.RecommendationInputs{}, contract.ErrRuntimeNotReady
	}
	return r.planning.Recommendations(ctx, request)
}
