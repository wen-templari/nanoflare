package runtime

import (
	"context"
	"encoding/hex"
	"net"
	"net/http"
	"strings"
)

// EnsuredWorker is a leased runtime endpoint. Release after the response body
// or upgraded connection closes so generation retirement can drain the request.
type EnsuredWorker struct {
	SocketPath string
	URL        string
	Token      string
	Port       int // Legacy TCP endpoints used by compatibility tests.
	ColdStart  bool
	Release    func()
}

// SocketHost gives each Unix socket a separate HTTP connection pool.
func SocketHost(path string) string { return hex.EncodeToString([]byte(path)) + ".unix" }

func ConfigureTransport(t *http.Transport) {
	t.Proxy = nil
	dialer := &net.Dialer{}
	t.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		host, _, err := net.SplitHostPort(address)
		if err == nil && strings.HasSuffix(host, ".unix") {
			path, err := hex.DecodeString(strings.TrimSuffix(host, ".unix"))
			if err != nil {
				return nil, err
			}
			return dialer.DialContext(ctx, "unix", string(path))
		}
		return dialer.DialContext(ctx, network, address)
	}
}
