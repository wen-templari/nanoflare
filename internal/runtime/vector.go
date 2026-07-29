package runtime

import (
	"context"
	"encoding/json"
	"net"
	"net/url"
	"strings"
	"sync/atomic"
	"time"

	"github.com/clas/nanoflare/internal/nanoflare"
)

// VectorForwarder sends structured runtime output to a local Vector socket.
// It intentionally never blocks the workerd process: unavailable collectors
// result in dropped events, which can be observed through Dropped().
type VectorForwarder struct {
	network string
	socket  string
	queue   chan nanoflare.WorkerOutputLine
	dropped atomic.Uint64
	stopped chan struct{}
}

func NewVectorForwarder(socket string, capacity int) *VectorForwarder {
	if strings.TrimSpace(socket) == "" {
		return nil
	}
	if capacity <= 0 {
		capacity = 1024
	}
	network, address := vectorAddress(socket)
	f := &VectorForwarder{network: network, socket: address, queue: make(chan nanoflare.WorkerOutputLine, capacity), stopped: make(chan struct{})}
	go f.run()
	return f
}

func (f *VectorForwarder) Append(line nanoflare.WorkerOutputLine) {
	if f == nil {
		return
	}
	select {
	case f.queue <- line:
	default:
		f.dropped.Add(1)
	}
}

func (f *VectorForwarder) Dropped() uint64 {
	if f == nil {
		return 0
	}
	return f.dropped.Load()
}

func (f *VectorForwarder) Close() {
	if f != nil {
		close(f.stopped)
	}
}

func (f *VectorForwarder) run() {
	for {
		select {
		case <-f.stopped:
			return
		case line := <-f.queue:
			f.write(line)
		}
	}
}

func (f *VectorForwarder) write(line nanoflare.WorkerOutputLine) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	connection, err := (&net.Dialer{}).DialContext(ctx, f.network, f.socket)
	if err != nil {
		f.dropped.Add(1)
		return
	}
	defer connection.Close()
	if err := json.NewEncoder(connection).Encode(line); err != nil {
		f.dropped.Add(1)
	}
}

func vectorAddress(value string) (string, string) {
	parsed, err := url.Parse(value)
	if err == nil && (parsed.Scheme == "tcp" || parsed.Scheme == "unix") {
		if parsed.Scheme == "tcp" {
			return "tcp", parsed.Host
		}
		return "unix", parsed.Path
	}
	return "unix", value
}
