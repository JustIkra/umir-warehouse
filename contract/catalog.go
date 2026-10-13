package contract

import "time"

type CatalogKind string

const (
	CatalogProduct CatalogKind = "product"
	CatalogDish    CatalogKind = "dish"
)

type IDSuggestion struct {
	EvaluatedAt  time.Time
	StateVersion int64
	Kind         CatalogKind
	SuggestedID  string
	Reserved     bool
}
