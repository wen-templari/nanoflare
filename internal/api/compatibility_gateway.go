package api

import (
	"net/http"
	"strings"
)

// NewCloudflareCompatibilityGateway redirects the Cloudflare API origin into an
// explicitly selected local account. Execution consent is configured on the
// server; ordinary bearer authentication and native authorization are still
// performed by the underlying adapter. Install
// on a private egress listener (or an explicitly selected CLI base URL), never
// as a general forwarding proxy. No request is sent to the public Internet.
func NewCloudflareCompatibilityGateway(adapter http.Handler, organization string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host := r.URL.Host
		if host == "" {
			host = r.Host
		}
		if r.URL.IsAbs() && (r.URL.Scheme != "https" || host != "api.cloudflare.com") {
			cfError(w, 403, "destination is not the Cloudflare API origin")
			return
		}
		for _, segment := range strings.Split(r.URL.Path, "/") {
			if segment == ".." || segment == "." {
				cfError(w, 403, "invalid destination path")
				return
			}
		}
		prefix := "/client/v4/accounts/" + organization + "/workers/"
		if organization == "" || !strings.HasPrefix(r.URL.EscapedPath(), prefix) {
			cfError(w, 403, "destination is outside the configured account")
			return
		}
		adapter.ServeHTTP(w, r)
	})
}
