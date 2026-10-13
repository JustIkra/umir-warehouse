package bootstrap

import (
	"context"

	"github.com/JustIkra/umir-warehouse/contract"
	"github.com/JustIkra/umir-warehouse/internal/application"
)

func (r *Runtime) AnalyticsStock(ctx context.Context, request contract.AnalyticsRequest) (contract.StockReport, error) {
	if err := r.enter(ctx); err != nil {
		return contract.StockReport{}, err
	}
	defer r.leave()
	if err := r.owner.ValidateAnalyticsScope(request.Scope); err != nil {
		if request.Cursor != "" {
			err = contract.ErrRefreshRequired
		}
		return contract.StockReport{}, err
	}
	if r.analytics == nil {
		return contract.StockReport{}, contract.ErrRuntimeNotReady
	}
	return r.analytics.Stock(ctx, request)
}
func (r *Runtime) AnalyticsMovements(ctx context.Context, request contract.AnalyticsRequest) (contract.MovementReport, error) {
	if err := r.enter(ctx); err != nil {
		return contract.MovementReport{}, err
	}
	defer r.leave()
	if err := r.owner.ValidateAnalyticsScope(request.Scope); err != nil {
		if request.Cursor != "" {
			err = contract.ErrRefreshRequired
		}
		return contract.MovementReport{}, err
	}
	if r.analytics == nil {
		return contract.MovementReport{}, contract.ErrRuntimeNotReady
	}
	return r.analytics.Movements(ctx, request)
}

type AnalyticsStockReader = application.AnalyticsStockReader

func (r *Runtime) AnalyticsStockUsing(ctx context.Context, request contract.AnalyticsRequest, reader AnalyticsStockReader) (contract.StockReport, error) {
	if err := r.enter(ctx); err != nil {
		return contract.StockReport{}, err
	}
	defer r.leave()
	if err := r.owner.ValidateAnalyticsScope(request.Scope); err != nil {
		if request.Cursor != "" {
			err = contract.ErrRefreshRequired
		}
		return contract.StockReport{}, err
	}
	if r.analytics == nil {
		return contract.StockReport{}, contract.ErrRuntimeNotReady
	}
	return r.analytics.StockUsing(ctx, request, reader)
}
