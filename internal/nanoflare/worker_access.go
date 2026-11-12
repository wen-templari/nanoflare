package nanoflare

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/url"
	"strings"
	"time"
)

const WorkerAccessHeader = "X-Nanoflare-Access-Token"
const workerAccessTTL = 5 * time.Minute

// WorkerAccessRequest authorizes one HTTP method and exact request URI, including query.
type WorkerAccessRequest struct {
	Method string `json:"method"`
	Path   string `json:"path"`
}

type WorkerAccessToken struct {
	Token     string    `json:"token"`
	URL       string    `json:"url"`
	ExpiresAt time.Time `json:"expires_at"`
}

// WorkerAccessClaims identify a machine principal, never an end-user session.
type WorkerAccessClaims struct {
	OrgID    string `json:"org_id"`
	WorkerID string `json:"worker_id"`
	Hostname string `json:"hostname"`
	Actor    string `json:"actor"`
	Method   string `json:"method"`
	Path     string `json:"path"`
	Expires  int64  `json:"exp"`
}

func ValidateWorkerAccessRequest(input WorkerAccessRequest) error {
	switch input.Method {
	case "GET", "HEAD", "POST", "PUT", "PATCH", "DELETE", "OPTIONS":
	default:
		return errors.New("unsupported HTTP method")
	}
	if len(input.Path) > 4096 {
		return errors.New("request path exceeds 4096 bytes")
	}
	u, err := url.ParseRequestURI(input.Path)
	if err != nil || !strings.HasPrefix(input.Path, "/") || strings.HasPrefix(input.Path, "//") || strings.ContainsAny(input.Path, "#\r\n\\") || u.IsAbs() || u.Host != "" {
		return errors.New("path must be an absolute request path, optionally including a query")
	}
	return nil
}

func (s *ControlAuthService) IssueWorkerAccessToken(app App, actor string, input WorkerAccessRequest) (WorkerAccessToken, error) {
	if err := ValidateWorkerAccessRequest(input); err != nil {
		return WorkerAccessToken{}, err
	}
	if len(s.secret) == 0 || app.OrgID == "" || app.ID == "" || app.Hostname == "" || app.IsPrivate() || actor == "" {
		return WorkerAccessToken{}, errors.New("worker access requires a public worker and an authenticated actor")
	}
	expires := s.now().UTC().Add(workerAccessTTL).Truncate(time.Second)
	claims := WorkerAccessClaims{OrgID: app.OrgID, WorkerID: app.ID, Hostname: app.Hostname, Actor: actor, Method: input.Method, Path: input.Path, Expires: expires.Unix()}
	payload, err := json.Marshal(claims)
	if err != nil {
		return WorkerAccessToken{}, err
	}
	encoded := base64.RawURLEncoding.EncodeToString(payload)
	signature := s.workerAccessSignature(encoded)
	return WorkerAccessToken{Token: "nfwa_" + encoded + "." + base64.RawURLEncoding.EncodeToString(signature), URL: "https://" + app.Hostname + input.Path, ExpiresAt: expires}, nil
}

func (s *ControlAuthService) workerAccessSignature(payload string) []byte {
	mac := hmac.New(sha256.New, s.secret)
	mac.Write([]byte("nanoflare-worker-access-v1\x00"))
	mac.Write([]byte(payload))
	return mac.Sum(nil)
}

func (s *ControlAuthService) ValidateWorkerAccessToken(token string) (WorkerAccessClaims, error) {
	var claims WorkerAccessClaims
	invalid := errors.New("invalid or expired worker access token")
	if len(s.secret) == 0 || !strings.HasPrefix(token, "nfwa_") || len(token) > 16384 {
		return claims, invalid
	}
	payload, encodedSignature, ok := strings.Cut(strings.TrimPrefix(token, "nfwa_"), ".")
	signature, err := base64.RawURLEncoding.DecodeString(encodedSignature)
	if !ok || err != nil || !hmac.Equal(signature, s.workerAccessSignature(payload)) {
		return claims, invalid
	}
	raw, err := base64.RawURLEncoding.DecodeString(payload)
	if err != nil || json.Unmarshal(raw, &claims) != nil {
		return WorkerAccessClaims{}, invalid
	}
	if claims.Expires <= s.now().UTC().Unix() || claims.OrgID == "" || claims.WorkerID == "" || claims.Hostname == "" || claims.Actor == "" || ValidateWorkerAccessRequest(WorkerAccessRequest{Method: claims.Method, Path: claims.Path}) != nil {
		return WorkerAccessClaims{}, invalid
	}
	return claims, nil
}
