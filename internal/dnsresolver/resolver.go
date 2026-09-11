// Package dnsresolver exposes host and custom DNS resolution to Workers over a
// loopback-only HTTP service.
package dnsresolver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"os"
	"strings"
	"time"
)

const systemProfile = "system"

type Profile struct {
	Resolver string   `json:"resolver,omitempty"`
	Servers  []string `json:"servers,omitempty"`
	Timeout  string   `json:"timeout,omitempty"`
}

type Config struct {
	DefaultProfile string             `json:"defaultProfile,omitempty"`
	Profiles       map[string]Profile `json:"profiles,omitempty"`
}

func (c Config) ProfileNames() []string {
	names := make([]string, 0, len(c.Profiles))
	for name := range c.Profiles {
		names = append(names, name)
	}
	return names
}

type Server struct {
	config Config
	server *http.Server
	ln     net.Listener
}

type lookupRequest struct {
	Hostname string `json:"hostname"`
	Family   int    `json:"family,omitempty"`
}

type lookupAddress struct {
	Address string `json:"address"`
	Family  int    `json:"family"`
}

type lookupResponse struct {
	Addresses []lookupAddress `json:"addresses,omitempty"`
	Code      string          `json:"code,omitempty"`
	Error     string          `json:"error,omitempty"`
}

// LoadConfig accepts either inline JSON or the path to a JSON file. An empty
// value enables the host resolver under the implicit "system" profile.
func LoadConfig(value string) (Config, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return normalizeConfig(Config{})
	}
	data := []byte(value)
	if !strings.HasPrefix(value, "{") {
		var err error
		data, err = os.ReadFile(value)
		if err != nil {
			return Config{}, fmt.Errorf("read DNS config: %w", err)
		}
	}
	var config Config
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&config); err != nil {
		return Config{}, fmt.Errorf("decode DNS config: %w", err)
	}
	return normalizeConfig(config)
}

func normalizeConfig(config Config) (Config, error) {
	if config.Profiles == nil {
		config.Profiles = make(map[string]Profile)
	}
	if _, ok := config.Profiles[systemProfile]; !ok {
		config.Profiles[systemProfile] = Profile{Resolver: systemProfile}
	}
	config.DefaultProfile = strings.TrimSpace(config.DefaultProfile)
	if config.DefaultProfile == "" {
		config.DefaultProfile = systemProfile
	}
	for name, profile := range config.Profiles {
		trimmed := strings.TrimSpace(name)
		if trimmed == "" || trimmed != name {
			return Config{}, errors.New("DNS profile names must be non-empty and may not have surrounding whitespace")
		}
		profile.Resolver = strings.TrimSpace(profile.Resolver)
		if profile.Resolver == "" && len(profile.Servers) == 0 {
			profile.Resolver = systemProfile
		}
		if profile.Resolver != "" && profile.Resolver != systemProfile {
			return Config{}, fmt.Errorf("DNS profile %q has unsupported resolver %q", name, profile.Resolver)
		}
		if profile.Resolver == systemProfile && len(profile.Servers) > 0 {
			return Config{}, fmt.Errorf("DNS profile %q cannot set both resolver and servers", name)
		}
		for i, server := range profile.Servers {
			server = strings.TrimSpace(server)
			if _, _, err := net.SplitHostPort(server); err != nil {
				return Config{}, fmt.Errorf("DNS profile %q server %q must include a port: %w", name, server, err)
			}
			profile.Servers[i] = server
		}
		if profile.Timeout != "" {
			duration, err := time.ParseDuration(profile.Timeout)
			if err != nil || duration <= 0 {
				return Config{}, fmt.Errorf("DNS profile %q has invalid timeout %q", name, profile.Timeout)
			}
		}
		config.Profiles[name] = profile
	}
	if _, ok := config.Profiles[config.DefaultProfile]; !ok {
		return Config{}, fmt.Errorf("default DNS profile %q is not configured", config.DefaultProfile)
	}
	return config, nil
}

func New(config Config, addr string) (*Server, error) {
	config, err := normalizeConfig(config)
	if err != nil {
		return nil, err
	}
	host, _, err := net.SplitHostPort(strings.TrimSpace(addr))
	if err != nil {
		return nil, fmt.Errorf("invalid DNS listen address: %w", err)
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return nil, errors.New("DNS listen address must use a loopback IP")
	}
	return &Server{config: config, server: &http.Server{Addr: addr}}, nil
}

func (s *Server) Start() error {
	ln, err := net.Listen("tcp", s.server.Addr)
	if err != nil {
		return fmt.Errorf("listen for Worker DNS: %w", err)
	}
	s.ln = ln
	s.server.Handler = http.HandlerFunc(s.serveHTTP)
	go func() { _ = s.server.Serve(ln) }()
	return nil
}

func (s *Server) Addr() string {
	if s.ln != nil {
		return s.ln.Addr().String()
	}
	return s.server.Addr
}

func (s *Server) Close(ctx context.Context) error { return s.server.Shutdown(ctx) }

func (s *Server) serveHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || r.URL.Path != "/lookup" {
		http.NotFound(w, r)
		return
	}
	var request lookupRequest
	decoder := json.NewDecoder(io.LimitReader(r.Body, 64<<10))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		writeJSON(w, http.StatusBadRequest, lookupResponse{Code: "EINVAL", Error: "invalid lookup request: " + err.Error()})
		return
	}
	request.Hostname = strings.TrimSpace(request.Hostname)
	if request.Hostname == "" || (request.Family != 0 && request.Family != 4 && request.Family != 6) {
		writeJSON(w, http.StatusBadRequest, lookupResponse{Code: "EINVAL", Error: "hostname and family (0, 4, or 6) are required"})
		return
	}
	profileName := strings.TrimSpace(r.Header.Get("X-Nanoflare-DNS-Profile"))
	if profileName == "" {
		profileName = s.config.DefaultProfile
	}
	profile, ok := s.config.Profiles[profileName]
	if !ok {
		writeJSON(w, http.StatusBadRequest, lookupResponse{Code: "EBADPROFILE", Error: fmt.Sprintf("DNS profile %q is not configured", profileName)})
		return
	}
	addresses, err := lookup(r.Context(), profile, request.Hostname, request.Family)
	if err != nil {
		code := "EAI_AGAIN"
		var dnsErr *net.DNSError
		if errors.As(err, &dnsErr) && dnsErr.IsNotFound {
			code = "ENOTFOUND"
		}
		if errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &dnsErr) && dnsErr.IsTimeout) {
			code = "ETIMEOUT"
		}
		writeJSON(w, http.StatusBadGateway, lookupResponse{Code: code, Error: err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, lookupResponse{Addresses: addresses})
}

func lookup(ctx context.Context, profile Profile, hostname string, family int) ([]lookupAddress, error) {
	timeout := 5 * time.Second
	if profile.Timeout != "" {
		timeout, _ = time.ParseDuration(profile.Timeout)
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	network := "ip"
	if family == 4 {
		network = "ip4"
	} else if family == 6 {
		network = "ip6"
	}
	resolvers := []*net.Resolver{net.DefaultResolver}
	if len(profile.Servers) > 0 {
		resolvers = resolvers[:0]
		for _, server := range profile.Servers {
			server := server
			resolvers = append(resolvers, &net.Resolver{PreferGo: true, Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, network, server)
			}})
		}
	}
	type result struct {
		ips []netip.Addr
		err error
	}
	results := make(chan result, len(resolvers))
	for _, resolver := range resolvers {
		go func(resolver *net.Resolver) {
			ips, err := resolver.LookupNetIP(ctx, network, hostname)
			results <- result{ips: ips, err: err}
		}(resolver)
	}
	var lastErr error
	for range resolvers {
		resolved := <-results
		if resolved.err != nil {
			lastErr = resolved.err
			continue
		}
		ips := resolved.ips
		result := make([]lookupAddress, 0, len(ips))
		seen := make(map[string]bool, len(ips))
		for _, ip := range ips {
			value := ip.String()
			if seen[value] {
				continue
			}
			seen[value] = true
			ipFamily := 6
			if ip.Is4() {
				ipFamily = 4
			}
			result = append(result, lookupAddress{Address: value, Family: ipFamily})
		}
		if len(result) > 0 {
			return result, nil
		}
		lastErr = &net.DNSError{Err: "no addresses", Name: hostname, IsNotFound: true}
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	return nil, lastErr
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
