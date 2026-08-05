// Package egress provides the private Worker outbound HTTP adapter.
package egress

import (
	"bufio"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
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

	"golang.org/x/net/http/httpproxy"
)

type Config struct {
	ProxyURL string
	CAFiles  []string
	NoProxy  []string
	Addr     string
}

type Server struct {
	listener     net.Listener
	addr         string
	transport    *http.Transport
	proxy        func(*http.Request) (*url.URL, error)
	dialer       *net.Dialer
	lookupIPAddr func(context.Context, string) ([]net.IPAddr, error)
	directNets   []*net.IPNet
	readTimeout  time.Duration
	connectTO    time.Duration
	resolveTO    time.Duration
	mu           sync.Mutex
	conns        map[net.Conn]bool
	cancels      map[net.Conn]context.CancelFunc
	shuttingDown bool
	wg           sync.WaitGroup
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

func ParseNoProxy(value string) []string {
	var entries []string
	for _, entry := range strings.Split(value, ",") {
		if entry = strings.TrimSpace(entry); entry != "" {
			entries = append(entries, entry)
		}
	}
	return entries
}

func New(config Config) (*Server, error) {
	proxyURL, err := parseProxyURL(config.ProxyURL)
	if err != nil {
		return nil, err
	}
	proxy := newProxySelector(proxyURL, config.NoProxy)
	roots, err := loadRoots(config.CAFiles)
	if err != nil {
		return nil, err
	}
	addr := strings.TrimSpace(config.Addr)
	if addr == "" {
		addr = "127.0.0.1:8082"
	}
	dialer := &net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}
	transport := &http.Transport{
		Proxy:           proxy,
		TLSClientConfig: &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12},
		// The adapter is an HTTP/1.1 proxy toward workerd. Speaking HTTP/1.1
		// upstream keeps responses framed with Content-Length or chunked
		// encoding; an HTTP/2 upstream response has no length delimiter and
		// would hang when relayed over the keep-alive HTTP/1.1 hop.
		ForceAttemptHTTP2:     false,
		DialContext:           dialer.DialContext,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 30 * time.Second,
		ExpectContinueTimeout: time.Second,
		IdleConnTimeout:       90 * time.Second,
	}
	return &Server{
		addr:         addr,
		transport:    transport,
		proxy:        proxy,
		dialer:       dialer,
		lookupIPAddr: net.DefaultResolver.LookupIPAddr,
		directNets:   parseDirectNets(config.NoProxy),
		readTimeout:  90 * time.Second,
		connectTO:    proxyConnectTimeout,
		resolveTO:    dnsResolveTimeout,
		conns:        make(map[net.Conn]bool),
		cancels:      make(map[net.Conn]context.CancelFunc),
	}, nil
}

// parseDirectNets extracts the CIDR entries from NO_PROXY so a raw-TCP CONNECT
// whose hostname *resolves* into one of those ranges is dialed directly, not
// just one that is written as an IP literal. This lets an operator declare
// corporate ranges (which may be publicly-routable, e.g. Bosch-owned blocks)
// once and have every hostname inside them treated as reachable-direct.
func parseDirectNets(noProxy []string) []*net.IPNet {
	var nets []*net.IPNet
	for _, entry := range noProxy {
		entry = strings.TrimSpace(entry)
		if !strings.Contains(entry, "/") {
			continue
		}
		if _, network, err := net.ParseCIDR(entry); err == nil {
			nets = append(nets, network)
		}
	}
	return nets
}

func newProxySelector(proxyURL *url.URL, noProxy []string) func(*http.Request) (*url.URL, error) {
	config := httpproxy.Config{
		HTTPProxy:  proxyURL.String(),
		HTTPSProxy: proxyURL.String(),
		NoProxy:    strings.Join(noProxy, ","),
	}
	selector := config.ProxyFunc()
	return func(request *http.Request) (*url.URL, error) {
		return selector(request.URL)
	}
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
	s.mu.Lock()
	s.shuttingDown = true
	for conn, idle := range s.conns {
		if idle {
			_ = conn.Close()
		}
	}
	s.mu.Unlock()
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
		for conn, cancel := range s.cancels {
			cancel()
			_ = conn.Close()
		}
		for conn := range s.conns {
			_ = conn.Close()
		}
		s.mu.Unlock()
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
		if s.shuttingDown {
			s.mu.Unlock()
			_ = conn.Close()
			continue
		}
		s.conns[conn] = true
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
		delete(s.cancels, conn)
		s.mu.Unlock()
	}()

	countingReader := &byteCountingReader{Reader: conn}
	reader := bufio.NewReader(countingReader)
	writer := bufio.NewWriter(conn)
	for {
		bufferedBefore := reader.Buffered()
		bytesReadBefore := countingReader.bytesRead
		_ = conn.SetReadDeadline(time.Now().Add(s.readTimeout))
		request, err := http.ReadRequest(reader)
		if err != nil {
			var netErr net.Error
			timedOut := errors.As(err, &netErr) && netErr.Timeout()
			idleTimeout := timedOut && bufferedBefore == 0 && countingReader.bytesRead == bytesReadBefore
			if !errors.Is(err, io.EOF) && !idleTimeout {
				response := errorResponse(http.StatusBadRequest, "bad request")
				_ = writeResponse(writer, response)
				response.Body.Close()
			}
			return
		}
		_ = conn.SetReadDeadline(time.Time{})
		if request.Method == http.MethodConnect {
			s.handleConnect(conn, reader, request)
			return
		}
		requestContext, cancel := context.WithCancel(request.Context())
		request = request.WithContext(requestContext)
		if !s.setActive(conn, cancel) {
			cancel()
			return
		}
		response := s.forward(request)
		if err := writeResponse(writer, response); err != nil {
			response.Body.Close()
			request.Body.Close()
			cancel()
			return
		}
		response.Body.Close()
		cancel()
		if request.Close || response.Close {
			request.Body.Close()
			return
		}
		if !s.drainRequestBody(conn, request) || !s.setIdle(conn) {
			return
		}
	}
}

func (s *Server) drainRequestBody(conn net.Conn, request *http.Request) bool {
	_ = conn.SetReadDeadline(time.Now().Add(s.readTimeout))
	_, readErr := io.Copy(io.Discard, request.Body)
	closeErr := request.Body.Close()
	_ = conn.SetReadDeadline(time.Time{})
	return readErr == nil && closeErr == nil
}

func (s *Server) setActive(conn net.Conn, cancel context.CancelFunc) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.shuttingDown {
		_ = conn.Close()
		return false
	}
	s.conns[conn] = false
	s.cancels[conn] = cancel
	return true
}

func (s *Server) setIdle(conn net.Conn) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.cancels, conn)
	if s.shuttingDown {
		_ = conn.Close()
		return false
	}
	s.conns[conn] = true
	return true
}

type byteCountingReader struct {
	io.Reader
	bytesRead int64
}

func (r *byteCountingReader) Read(p []byte) (int, error) {
	n, err := r.Reader.Read(p)
	r.bytesRead += int64(n)
	return n, err
}

// handleConnect tunnels a raw TCP CONNECT (used by workerd's Socket API, e.g.
// MQTT and PostgreSQL). The corporate proxy only carries HTTP(S) to the public
// internet and refuses CONNECT to non-443 ports, so any target on the corporate
// or private network must be dialed directly. Routing: an explicit NO_PROXY
// match, or a target that resolves entirely to loopback/private/configured
// ranges, is dialed directly; everything else tunnels through the proxy with a
// nested CONNECT. The adapter never terminates TLS for a tunnel; workerd does
// that end to end.
func (s *Server) handleConnect(conn net.Conn, reader *bufio.Reader, request *http.Request) {
	target := request.Host
	if target == "" {
		target = request.URL.Host
	}
	if target == "" {
		_, _ = io.WriteString(conn, "HTTP/1.1 400 Bad Request\r\n\r\n")
		return
	}

	proxyURL, err := s.proxy(&http.Request{URL: &url.URL{Scheme: "https", Host: target}})
	if err != nil {
		_, _ = io.WriteString(conn, "HTTP/1.1 502 Bad Gateway\r\n\r\n")
		return
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if !s.setActive(conn, cancel) {
		return
	}

	var upstream net.Conn
	if proxyURL == nil || s.resolvesDirect(ctx, target) {
		upstream, err = s.dialer.DialContext(ctx, "tcp", target)
	} else {
		upstream, err = s.dialProxyConnect(ctx, proxyURL, target)
	}
	if err != nil {
		_, _ = io.WriteString(conn, "HTTP/1.1 502 Bad Gateway\r\n\r\n")
		return
	}
	defer upstream.Close()
	go func() {
		<-ctx.Done()
		_ = upstream.Close()
	}()

	if _, err := io.WriteString(conn, "HTTP/1.1 200 Connection Established\r\n\r\n"); err != nil {
		return
	}

	done := make(chan struct{}, 2)
	go func() { _, _ = io.Copy(upstream, reader); _ = upstream.Close(); done <- struct{}{} }()
	go func() { _, _ = io.Copy(conn, upstream); _ = conn.Close(); done <- struct{}{} }()
	<-done
	<-done
}

// proxyConnectTimeout bounds the nested CONNECT handshake with the corporate
// proxy. Without it, a proxy that accepts the TCP connection but never answers
// CONNECT (common when it refuses non-443 ports such as MQTT 1883/3003 or
// PostgreSQL 5432) would block the read forever, wedging the tunnel and hanging
// the Worker that opened the socket.
const proxyConnectTimeout = 15 * time.Second

// dnsResolveTimeout bounds the DNS lookup used to classify a CONNECT target as
// direct-dial vs. proxied. A slow resolver must not stall opening the socket.
const dnsResolveTimeout = 5 * time.Second

// resolvesDirect reports whether a raw-TCP CONNECT target should be dialed
// directly rather than tunneled through the corporate proxy. An IP-literal
// target is classified without DNS; a hostname is resolved and treated as
// direct only when every resolved address is direct-dial (loopback, private,
// link-local, or inside a configured NO_PROXY CIDR). Requiring all addresses to
// be direct keeps a genuinely public host on the proxy path; a lookup failure
// is treated as "not direct" so the proxy path (and its fast-fail timeout)
// applies. This captures bare corporate hostnames (e.g. a source database) that
// resolve into private space without being listed in NO_PROXY.
func (s *Server) resolvesDirect(ctx context.Context, target string) bool {
	host := target
	if h, _, err := net.SplitHostPort(target); err == nil {
		host = h
	}
	if ip := net.ParseIP(host); ip != nil {
		return s.isDirectIP(ip)
	}
	lookupCtx, cancel := context.WithTimeout(ctx, s.resolveTO)
	defer cancel()
	addrs, err := s.lookupIPAddr(lookupCtx, host)
	if err != nil || len(addrs) == 0 {
		return false
	}
	for _, addr := range addrs {
		if !s.isDirectIP(addr.IP) {
			return false
		}
	}
	return true
}

// isDirectIP reports whether an address is on the local or corporate network and
// so must be dialed directly (the proxy cannot reach it, and would refuse a
// non-443 CONNECT to it).
func (s *Server) isDirectIP(ip net.IP) bool {
	if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() {
		return true
	}
	for _, network := range s.directNets {
		if network.Contains(ip) {
			return true
		}
	}
	return false
}

// dialProxyConnect opens a CONNECT tunnel to target through the corporate proxy
// and returns the tunneled connection.
func (s *Server) dialProxyConnect(ctx context.Context, proxyURL *url.URL, target string) (net.Conn, error) {
	conn, err := s.dialer.DialContext(ctx, "tcp", proxyURL.Host)
	if err != nil {
		return nil, err
	}
	// Fail fast if the proxy never completes the CONNECT handshake; cleared
	// below once established so the splice runs without a deadline.
	_ = conn.SetDeadline(time.Now().Add(s.connectTO))
	var head strings.Builder
	fmt.Fprintf(&head, "CONNECT %s HTTP/1.1\r\nHost: %s\r\n", target, target)
	if proxyURL.User != nil {
		if password, ok := proxyURL.User.Password(); ok {
			credential := base64.StdEncoding.EncodeToString([]byte(proxyURL.User.Username() + ":" + password))
			fmt.Fprintf(&head, "Proxy-Authorization: Basic %s\r\n", credential)
		}
	}
	head.WriteString("\r\n")
	if _, err := io.WriteString(conn, head.String()); err != nil {
		_ = conn.Close()
		return nil, err
	}
	reader := bufio.NewReader(conn)
	response, err := http.ReadResponse(reader, &http.Request{Method: http.MethodConnect})
	if err != nil {
		_ = conn.Close()
		return nil, err
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusOK {
		_ = conn.Close()
		return nil, fmt.Errorf("proxy CONNECT to %s: %s", target, response.Status)
	}
	_ = conn.SetDeadline(time.Time{}) // hand a clean connection to the splice
	if reader.Buffered() > 0 {
		return &bufferedConn{Conn: conn, reader: io.MultiReader(reader, conn)}, nil
	}
	return conn, nil
}

// bufferedConn preserves bytes the proxy sent immediately after the CONNECT
// response so no tunneled data is lost.
type bufferedConn struct {
	net.Conn
	reader io.Reader
}

func (c *bufferedConn) Read(p []byte) (int, error) { return c.reader.Read(p) }

func (s *Server) forward(request *http.Request) *http.Response {
	if request.Method == http.MethodConnect || !request.URL.IsAbs() || (request.URL.Scheme != "http" && request.URL.Scheme != "https") {
		return errorResponse(http.StatusBadRequest, "absolute HTTP or HTTPS URL required")
	}
	out := request.Clone(request.Context())
	out.RequestURI = ""
	out.Host = out.URL.Host
	out.Body = noCloseReadCloser{request.Body}
	removeHopHeaders(out.Header)
	response, err := s.transport.RoundTrip(out)
	if err != nil {
		return errorResponse(http.StatusBadGateway, "egress request failed")
	}
	removeHopHeaders(response.Header)
	frameResponse(response)
	return response
}

// frameResponse guarantees the response is self-delimiting when relayed over
// the keep-alive HTTP/1.1 hop to workerd. A known Content-Length or existing
// chunked encoding already marks where the body ends, so the connection can be
// reused as-is. A body with an unknown length carries no HTTP/1.1 delimiter of
// its own — this happens for an HTTP/2 upstream (framed by DATA frames) and for
// an HTTP/1.x response delimited only by connection close — so we add chunked
// framing. Without this, workerd would read headers and then wait forever for a
// body end that never comes (the original pokeapi/HTTP/2 hang).
func frameResponse(response *http.Response) {
	response.Close = false
	if response.ContentLength < 0 && !hasChunkedEncoding(response.TransferEncoding) && responseHasBody(response) {
		response.TransferEncoding = []string{"chunked"}
	}
}

func hasChunkedEncoding(encodings []string) bool {
	for _, encoding := range encodings {
		if encoding == "chunked" {
			return true
		}
	}
	return false
}

// responseHasBody reports whether the response is allowed to carry a body, per
// RFC 9110: 1xx/204/304 statuses and responses to HEAD never do, so they must
// not be given chunked framing.
func responseHasBody(response *http.Response) bool {
	if response.Request != nil && response.Request.Method == http.MethodHead {
		return false
	}
	switch {
	case response.StatusCode >= 100 && response.StatusCode < 200:
		return false
	case response.StatusCode == http.StatusNoContent:
		return false
	case response.StatusCode == http.StatusNotModified:
		return false
	}
	return true
}

type noCloseReadCloser struct {
	io.Reader
}

func (noCloseReadCloser) Close() error { return nil }

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
	for _, value := range header.Values("Connection") {
		for _, name := range strings.Split(value, ",") {
			header.Del(strings.TrimSpace(name))
		}
	}
	for _, name := range []string{"Connection", "Proxy-Connection", "Keep-Alive", "Proxy-Authenticate", "Proxy-Authorization", "Te", "Trailer", "Transfer-Encoding", "Upgrade"} {
		header.Del(name)
	}
}
