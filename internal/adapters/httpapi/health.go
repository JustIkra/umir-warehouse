package httpapi

import (
	"net/http"

	"github.com/JustIkra/umir-warehouse/contract"
)

func NewHealthOnlyHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		if r.URL.Path == "/healthz" && r.Method == http.MethodGet && r.URL.RawQuery == "" {
			writeJSON(w, http.StatusOK, struct {
				Status  string `json:"status"`
				Service string `json:"service"`
			}{"ok", "umir-warehouse"})
			return
		}
		writeScopedError(w, contract.ErrInvalidArgument, r.Method != http.MethodGet)
	})
}
