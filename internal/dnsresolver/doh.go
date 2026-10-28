package dnsresolver

import (
	"bytes"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

const (
	DoHHost = "cloudflare-dns.com"
	DoHPath = "/dns-query"
)

var dohRecordTypes = map[string]int{
	"A": 1, "AAAA": 28, "TXT": 16, "MX": 15, "SRV": 33, "NS": 2, "CNAME": 5, "SOA": 6, "PTR": 12, "ANY": 255, "CAA": 257, "NAPTR": 35,
}

type dohAnswer struct {
	Name string `json:"name"`
	Type int    `json:"type"`
	TTL  int    `json:"TTL"`
	Data string `json:"data"`
}
type dohResponse struct {
	Status   int           `json:"Status"`
	Question []dohQuestion `json:"Question"`
	Answer   []dohAnswer   `json:"Answer,omitempty"`
}

type dohQuestion struct {
	Name string `json:"name"`
	Type int    `json:"type"`
}

// serveDoH implements the JSON subset used by the pinned workerd node:dns.
// This is a host-lookup adapter, not a general DNS wire-protocol endpoint.
func (s *Server) serveDoH(w http.ResponseWriter, r *http.Request) {
	if r.Context().Err() != nil {
		return
	}
	hostname := strings.TrimSpace(r.URL.Query().Get("name"))
	typeName := strings.ToUpper(r.URL.Query().Get("type"))
	typeCode, _ := strconv.Atoi(typeName)
	if value := dohRecordTypes[typeName]; value != 0 {
		typeCode = value
	}
	// workerd validates Question even for errors before examining Answer.
	response := dohResponse{Question: []dohQuestion{{Name: hostname, Type: typeCode}}}
	profile, ok := s.profile(r)
	if hostname == "" || !ok {
		response.Status = 2
	} else if !supportedDoHType(typeCode) {
		response.Status = 4
	} else {
		var err error
		response.Answer, err = lookupDoH(r.Context(), profile, hostname, typeCode)
		if err != nil {
			response.Status = 2
			var dnsErr *net.DNSError
			if errors.As(err, &dnsErr) && dnsErr.IsNotFound {
				response.Status = 3
			}
		} else if len(response.Answer) == 0 {
			// Avoid workerd's undefined result on an empty successful A answer.
			response.Status = 3
		}
	}

	if r.Context().Err() != nil {
		return
	}
	writeJSON(w, http.StatusOK, response)
}

func (s *Server) profile(r *http.Request) (Profile, bool) {
	name := strings.TrimSpace(r.Header.Get("X-Nanoflare-DNS-Profile"))
	if name == "" {
		name = s.config.DefaultProfile
	}
	profile, ok := s.config.Profiles[name]
	return profile, ok
}

func IsDoHRequest(r *http.Request) bool {
	return r.Method == http.MethodGet && r.URL.Scheme == "https" &&
		strings.EqualFold(strings.TrimSuffix(r.URL.Hostname(), "."), DoHHost) &&
		(r.URL.Port() == "" || r.URL.Port() == "443") && r.URL.User == nil &&
		r.URL.EscapedPath() == DoHPath && r.URL.Query().Has("name")
}

type dohTransport struct {
	next     http.RoundTripper
	resolver http.Handler
}

// DoHTransport answers workerd's DoH through the platform default resolver.
// Invoke the private handler in-process so split deployments need no listener,
// and HTTP(S)_PROXY cannot send private names outside the platform. Broker
// authorization, effect accounting and cancellation happen before RoundTrip.
// A nil resolver fails closed for DoH; other requests retain normal egress.
func DoHTransport(next http.RoundTripper, resolver http.Handler) http.RoundTripper {
	if next == nil {
		next = http.DefaultTransport
	}
	return &dohTransport{next: next, resolver: resolver}
}
func (t *dohTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if !IsDoHRequest(r) {
		return t.next.RoundTrip(r)
	}
	if err := r.Context().Err(); err != nil {
		return nil, err
	}
	// Only the requested name/type cross this boundary. Caller-controlled
	// profiles, authorization and other headers must never reach the resolver.
	query := url.Values{"name": {r.URL.Query().Get("name")}, "type": {r.URL.Query().Get("type")}}
	request, err := http.NewRequestWithContext(r.Context(), http.MethodGet, "http://nanoflare.dns"+DoHPath+"?"+query.Encode(), nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Accept", "application/dns-json")
	response := &dohBuffer{header: make(http.Header)}
	if t.resolver == nil {
		writeJSON(response, http.StatusServiceUnavailable, dohResponse{Status: 2})
	} else {
		t.resolver.ServeHTTP(response, request)
	}
	if err := r.Context().Err(); err != nil {
		return nil, err
	}
	status := response.status
	if status == 0 {
		status = http.StatusOK
	}
	return &http.Response{StatusCode: status, Header: response.header, Body: io.NopCloser(&response.body), ContentLength: int64(response.body.Len()), Request: r}, nil
}

type dohBuffer struct {
	header http.Header
	status int
	body   bytes.Buffer
}

func (b *dohBuffer) Header() http.Header { return b.header }
func (b *dohBuffer) WriteHeader(status int) {
	if b.status == 0 {
		b.status = status
	}
}
func (b *dohBuffer) Write(data []byte) (int, error) {
	if b.status == 0 {
		b.WriteHeader(http.StatusOK)
	}
	return b.body.Write(data)
}
