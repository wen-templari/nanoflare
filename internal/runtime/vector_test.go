package runtime

import (
	"net"
	"strings"
	"testing"
	"time"

	"github.com/clas/nanoflare/internal/nanoflare"
)

func TestVectorForwarderBoundsStalledWriteAndJoins(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	accepted, release := make(chan struct{}), make(chan struct{})
	go func() {
		connection, err := listener.Accept()
		if err != nil {
			return
		}
		defer connection.Close()
		close(accepted)
		<-release // Deliberately never read the collector stream.
	}()
	forwarder := NewVectorForwarder("tcp://"+listener.Addr().String(), 1)
	defer func() { close(release); forwarder.Close() }()
	// Large embedding-origin output ensures write backpressure, independent of
	// OS send-buffer size. Customer runtime logs are bounded earlier in telemetry.
	forwarder.Append(nanoflare.WorkerOutputLine{Message: strings.Repeat("x", 16<<20)})
	select {
	case <-accepted:
	case <-time.After(time.Second):
		t.Fatal("collector not connected")
	}
	done := make(chan struct{})
	go func() { forwarder.Close(); close(done) }()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("stalled collector blocked forwarder shutdown")
	}
	if forwarder.Dropped() != 1 {
		t.Fatalf("failed write drop count: %d", forwarder.Dropped())
	}
	forwarder.Close() // Shutdown is idempotent, including joined callers.
	forwarder.Append(nanoflare.WorkerOutputLine{Message: "after stop"})
	if forwarder.Dropped() != 2 {
		t.Fatal("event accepted after shutdown")
	}
}
