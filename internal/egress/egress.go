// Package egress provides the private Worker outbound HTTP adapter.
package egress

import (
	"bufio"
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
	"sync"
	"time"
)

type Config struct {
	ProxyURL string
	CAFiles  []string
	Addr     string
}

type Server struct {
	listener  net.Listener
	addr      string
	transport *http.Transport
	mu        sync.Mutex
	conns     map[net.Conn]struct{}
	wg        sync.WaitGroup
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
	return &Server{addr: addr, transport: transport, conns: make(map[net.Conn]struct{})}, nil
}

func (s *Server) Start() error {
	host, _, err := net.SplitHostPort(s.addr)
	if err != nil {
		return fmt.Errorf("invalid egress listen address: %w", err)
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return errors.New("egress listen address must use a loopback IP")
	}
	s.listener, err = net.Listen("tcp", s.addr)
	if err != nil {
		return fmt.Errorf("listen for Worker egress: %w", err)
	}
	s.wg.Add(1)
	go s.accept()
	return nil
}

func (s *Server) Addr() string { return s.listener.Addr().String() }

func (s *Server) Close(ctx context.Context) error {
	s.transport.CloseIdleConnections()
	if s.listener == nil {
		return nil
	}
	err := s.listener.Close()
	done := make(chan struct{})
	go func() {
		s.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		return normalizeCloseError(err)
	case <-ctx.Done():
		s.mu.Lock()
		for conn := range s.conns {
			_ = conn.Close()
		}
		s.mu.Unlock()
		<-done
		return ctx.Err()
	}
}

func (s *Server) accept() {
	defer s.wg.Done()
	for {
		conn, err := s.listener.Accept()
		if err != nil {
			return
		}
		s.mu.Lock()
		s.conns[conn] = struct{}{}
		s.mu.Unlock()
		s.wg.Add(1)
		go s.serveConn(conn)
	}
}

func (s *Server) serveConn(conn net.Conn) {
	defer s.wg.Done()
	defer conn.Close()
	defer func() {
		s.mu.Lock()
		delete(s.conns, conn)
		s.mu.Unlock()
	}()

	reader := bufio.NewReader(conn)
	writer := bufio.NewWriter(conn)
	for {
		_ = conn.SetReadDeadline(time.Now().Add(10 * time.Second))
		request, err := http.ReadRequest(reader)
		if err != nil {
			if !errors.Is(err, io.EOF) {
				_ = writeResponse(writer, errorResponse(http.StatusBadRequest, "bad request"))
			}
			return
		}
		_ = conn.SetReadDeadline(time.Time{})
		response := s.forward(request)
		if err := writeResponse(writer, response); err != nil {
			response.Body.Close()
			return
		}
		response.Body.Close()
		if request.Close || response.Close {
			return
		}
	}
}

func (s *Server) forward(request *http.Request) *http.Response {
	if request.Method == http.MethodConnect || !request.URL.IsAbs() || (request.URL.Scheme != "http" && request.URL.Scheme != "https") {
		return errorResponse(http.StatusBadRequest, "absolute HTTP or HTTPS URL required")
	}
	out := request.Clone(request.Context())
	out.RequestURI = ""
	out.Host = out.URL.Host
	out.Header = request.Header.Clone()
	removeHopHeaders(out.Header)
	response, err := s.transport.RoundTrip(out)
	if err != nil {
		return errorResponse(http.StatusBadGateway, "egress request failed")
	}
	removeHopHeaders(response.Header)
	return response
}

func writeResponse(writer *bufio.Writer, response *http.Response) error {
	if err := response.Write(writer); err != nil {
		return err
	}
	return writer.Flush()
}

func errorResponse(status int, message string) *http.Response {
	body := message + "\n"
	return &http.Response{
		StatusCode:    status,
		Status:        fmt.Sprintf("%d %s", status, http.StatusText(status)),
		Proto:         "HTTP/1.1",
		ProtoMajor:    1,
		ProtoMinor:    1,
		Header:        http.Header{"Content-Type": {"text/plain; charset=utf-8"}},
		Body:          io.NopCloser(strings.NewReader(body)),
		ContentLength: int64(len(body)),
		Close:         true,
	}
}

func normalizeCloseError(err error) error {
	if errors.Is(err, net.ErrClosed) {
		return nil
	}
	return err
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
