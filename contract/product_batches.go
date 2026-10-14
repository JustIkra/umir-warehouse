package contract

type ProductBatchesRequest struct {
	Scope                    Scope
	Actor, ProductID, Cursor string
	Limit                    int
}

type ProductBatchRow struct {
	Batch    Batch
	BaseUnit BaseUnit
}

type ProductBatchesPage struct {
	Identity             ReportIdentity
	ProductID            string
	Items                []ProductBatchRow
	NextCursor           string
	InvalidationRequired bool
}
