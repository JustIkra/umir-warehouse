package bootstrap

import (
	"net/http"

	"github.com/JustIkra/umir-warehouse/contract"

	"github.com/JustIkra/umir-warehouse/internal/adapters/httpapi"
	"github.com/JustIkra/umir-warehouse/internal/adapters/zonedata"
)

type JWTOptions = httpapi.JWTOptions
type ScopedHTTPOptions = httpapi.ScopedOptions
type ScopedRuntime = httpapi.Runtime
type ScopedDirectory = httpapi.TenantDirectory

func NewJWTVerifier(options JWTOptions) (*httpapi.JWTVerifier, error) {
	return httpapi.NewJWTVerifier(options)
}
func NewScopedHTTPHandler(registry ScopedDirectory, options ScopedHTTPOptions) (http.Handler, error) {
	if err := zonedata.Check(); err != nil {
		return nil, err
	}
	if options.TZDataVersion != "" && options.TZDataVersion != zonedata.Version {
		return nil, contract.ErrTimeZoneDataUnavailable
	}
	options.TZDataVersion = zonedata.Version
	return httpapi.NewScopedHandler(registry, options)
}

func NewHealthOnlyHandler() http.Handler { return httpapi.NewHealthOnlyHandler() }

const TimeZoneDataVersion = zonedata.Version
