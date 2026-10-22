package api

import (
	"net/http"
	"net/url"
	"strings"

	"github.com/clas/nanoflare/internal/cfcompat"
	"github.com/clas/nanoflare/internal/nanoflare"
)

func (s *Server) cfCreateAssetSession(w http.ResponseWriter, r *http.Request, ns nanoflare.DispatchNamespace, script string) {
	if len(r.Header.Values("X-Nanoflare-Customer-ID")) > 1 {
		cfError(w, 422, "duplicate customer identity header")
		return
	}
	data, err := cfMutationBody(r)
	if err != nil {
		cfError(w, 422, err.Error())
		return
	}
	manifest, err := cfcompat.DecodeAssetManifest(data)
	if err != nil {
		cfError(w, 422, err.Error())
		return
	}
	result, err := s.service.CreateAssetUploadForOrg(controlOrgID(r), ns.ID, script, r.Header.Get("X-Nanoflare-Customer-ID"), manifest)
	if err != nil {
		cfServiceError(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	cfJSON(w, result)
}

// This endpoint accepts a delegated upload credential, never a control token.
// It can stage only verified manifest bytes and cannot publish executable code.
func (s *Server) cfAssetFiles(w http.ResponseWriter, r *http.Request, org string) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Nanoflare-Execution-Profile", "trusted_shared_workerd")
	if r.Method != "POST" {
		cfError(w, 422, "asset uploads require POST")
		return
	}
	query, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil || len(query) != 1 || len(query["base64"]) != 1 || query.Get("base64") != "true" {
		cfError(w, 422, "asset uploads require only base64=true")
		return
	}
	parts := strings.Fields(r.Header.Get("Authorization"))
	if len(r.Header.Values("Authorization")) != 1 || len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
		cfError(w, 401, "asset upload credential is required")
		return
	}
	if err := s.service.ValidateAssetUploadToken(org, parts[1]); err != nil {
		cfError(w, 401, "asset upload credential is unavailable or expired")
		return
	}
	files, err := cfcompat.DecodeAssetFiles(r.Header.Get("Content-Type"), r.Body)
	if err != nil {
		cfError(w, 422, err.Error())
		return
	}
	result, err := s.service.UploadAssetFiles(org, parts[1], files)
	if err != nil {
		cfServiceError(w, err)
		return
	}
	status := http.StatusOK
	if len(result.Buckets) == 0 {
		status = http.StatusCreated
	}
	writeJSON(w, status, cfEnvelope{Success: true, Errors: []cfAPIError{}, Messages: []cfAPIError{}, Result: result})
}
