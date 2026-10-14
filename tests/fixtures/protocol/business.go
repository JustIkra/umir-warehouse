package protocol

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
)

type requestStateKey struct{}
type requestState struct {
	operation, command string
	drop               bool
}

func BusinessHandler(handler http.Handler, _ *OwnerFaults) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		state := &requestState{}
		parts := strings.Split(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "), ".")
		if len(parts) == 3 {
			raw, err := base64.RawURLEncoding.DecodeString(parts[1])
			if err == nil {
				var metadata struct {
					ID        string `json:"jti"`
					Operation string `json:"operation"`
				}
				if json.Unmarshal(raw, &metadata) == nil {
					state.command = metadata.ID
					state.operation = metadata.Operation
				}
			}
		}
		r = r.WithContext(context.WithValue(r.Context(), requestStateKey{}, state))
		reply := httptest.NewRecorder()
		handler.ServeHTTP(reply, r)
		if state.drop {
			if hijacker, ok := w.(http.Hijacker); ok {
				conn, _, err := hijacker.Hijack()
				if err == nil {
					if err := conn.Close(); err != nil {
						return
					}
					return
				}
			}
			panic(http.ErrAbortHandler)
		}
		for key, values := range reply.Header() {
			w.Header()[key] = append([]string(nil), values...)
		}
		w.WriteHeader(reply.Code)
		if _, err := w.Write(reply.Body.Bytes()); err != nil {
			return
		}
	})
}
