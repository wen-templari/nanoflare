package api

import (
	"net/http"
	"strings"

	"github.com/clas/nanoflare/internal/nanoflare"
)

// Only the private resource handler redeems a revision grant. Business service
// methods receive an operation token, pinned until ServeHTTP finishes writing.
func beginResourceGrant(w http.ResponseWriter, r *http.Request, service *nanoflare.Service, kind nanoflare.ExecutionResourceKind, id string) (*http.Request, func(), bool) {
	token := bearerToken(r)
	if !strings.HasPrefix(token, "nfgrant_") {
		return r, func() {}, true
	}
	lease, err := service.Executions().AcquireResource(token, kind, strings.TrimSpace(id))
	if err != nil {
		writeRuntimeError(w, err)
		return r, nil, false
	}
	request := r.Clone(r.Context())
	request.Header.Set("Authorization", "Bearer "+lease.Token())
	return request, lease.Release, true
}
