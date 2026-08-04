// Package egress provides the private Worker outbound HTTP adapter.
package egress

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

type Config struct {
	ProxyURL string
	CAFiles  []string
	Addr     string
}

type Server struct {
	listener  net.Listener
	server    *http.Server
	transport *http.Transport
}

func ParseCAFiles(value string) []string {
	var files []string
	for _, file := range strings.Split(value, ",") {
		if file = strings.TrimSpace(file); file != "" {
			files = append(files, file)
		}
	}
	return files
}

func New(config Config) (*Server, error) {
	proxyURL, err := parseProxyURL(config.ProxyURL)
	if err != nil {
		return nil, err
	}
	roots, err := loadRoots(config.CAFiles)
	if err != nil {
		return nil, err
	}
	addr := strings.TrimSpace(config.Addr)
	if addr == "" {
		addr = "127.0.0.1:8082"
	}
	transport := &http.Transport{
		Proxy:                 http.ProxyURL(proxyURL),
		TLSClientConfig:       &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12},
		ForceAttemptHTTP2:     true,
		DialContext:           (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 30 * time.Second,
		ExpectContinueTimeout: time.Second,
		IdleConnTimeout:       90 * time.Second,
	}
	s := &Server{transport: transport}
	s.server = &http.Server{Addr: addr, Handler: http.HandlerFunc(s.serveHTTP), ReadHeaderTimeout: 10 * time.Second}
	return s, nil
}

func (s *Server) Start() error {
	host, _, err := net.SplitHostPort(s.server.Addr)
	if err != nil {
		return fmt.Errorf("invalid egress listen address: %w", err)
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return errors.New("egress listen address must use a loopback IP")
	}
	s.listener, err = net.Listen("tcp", s.server.Addr)
	if err != nil {
		return fmt.Errorf("listen for Worker egress: %w", err)
	}
	go func() { _ = s.server.Serve(s.listener) }()
	return nil
}

func (s *Server) Addr() string { return s.listener.Addr().String() }

func (s *Server) Close(ctx context.Context) error {
	s.transport.CloseIdleConnections()
	if s.listener == nil {
		return nil
	}
	return s.server.Shutdown(ctx)
}

func (s *Server) serveHTTP(w http.ResponseWriter, request *http.Request) {
	if request.Method == http.MethodConnect || !request.URL.IsAbs() || (request.URL.Scheme != "http" && request.URL.Scheme != "https") {
		http.Error(w, "absolute HTTP or HTTPS URL required", http.StatusBadRequest)
		return
	}
	out := request.Clone(request.Context())
	out.RequestURI = ""
	out.Header = request.Header.Clone()
	removeHopHeaders(out.Header)
	response, err := s.transport.RoundTrip(out)
	if err != nil {
		http.Error(w, "egress request failed", http.StatusBadGateway)
		return
	}
	defer response.Body.Close()
	removeHopHeaders(response.Header)
	for name, values := range response.Header {
		for _, value := range values {
			w.Header().Add(name, value)
		}
	}
	w.WriteHeader(response.StatusCode)
	_, _ = io.Copy(w, response.Body)
}

func parseProxyURL(value string) (*url.URL, error) {
	parsed, err := url.Parse(strings.TrimSpace(value))
	if err != nil || parsed.Scheme != "http" || parsed.Host == "" {
		return nil, errors.New("Worker egress proxy URL must be a valid http URL with a hostname")
	}
	if parsed.Hostname() == "" || parsed.Fragment != "" || (parsed.Path != "" && parsed.Path != "/") || parsed.RawQuery != "" {
		return nil, errors.New("Worker egress proxy URL must not contain a path, query, or fragment")
	}
	return parsed, nil
}

func loadRoots(files []string) (*x509.CertPool, error) {
	pool, err := x509.SystemCertPool()
	if err != nil {
		pool = x509.NewCertPool()
	}
	for _, file := range files {
		contents, err := os.ReadFile(file)
		if err != nil {
			return nil, fmt.Errorf("read egress CA file %q: %w", file, err)
		}
		remaining, certificates := contents, 0
		for len(remaining) > 0 {
			block, rest := pem.Decode(remaining)
			if block == nil {
				break
			}
			remaining = rest
			if block.Type == "CERTIFICATE" {
				certificate, err := x509.ParseCertificate(block.Bytes)
				if err != nil {
					return nil, fmt.Errorf("parse egress CA file %q: %w", file, err)
				}
				pool.AddCert(certificate)
				certificates++
			}
		}
		if certificates == 0 {
			return nil, fmt.Errorf("egress CA file %q contains no valid certificates", file)
		}
	}
	return pool, nil
}

func removeHopHeaders(header http.Header) {
	for _, name := range []string{"Connection", "Proxy-Connection", "Keep-Alive", "Proxy-Authenticate", "Proxy-Authorization", "Te", "Trailer", "Transfer-Encoding", "Upgrade"} {
		header.Del(name)
	}
}
