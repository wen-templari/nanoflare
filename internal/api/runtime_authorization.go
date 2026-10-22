package api

import (
	"context"
	"net/http"

	"github.com/clas/nanoflare/internal/nanoflare"
)

type runtimePrincipalKey struct{}

func authorizeRuntime(w http.ResponseWriter, r *http.Request, service *nanoflare.Service, kind, resource string) *http.Request {
	p, err := service.AuthorizeRuntime(r.Context(), bearerToken(r), kind, resource)
	if err != nil {
		writeRuntimeError(w, err)
		return nil
	}
	return r.WithContext(context.WithValue(r.Context(), runtimePrincipalKey{}, p))
}

func runtimePrincipal(r *http.Request) nanoflare.RuntimePrincipal {
	return r.Context().Value(runtimePrincipalKey{}).(nanoflare.RuntimePrincipal)
}
