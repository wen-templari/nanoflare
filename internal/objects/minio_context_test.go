package objects

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/clas/nanoflare/internal/nanoflare"
	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

var _ nanoflare.ContextObjectStore = (*MinIO)(nil)

func TestMinIOCancellationStopsStalledTransport(t *testing.T) {
	for _, operation := range []string{"get", "head", "put", "delete", "list"} {
		t.Run(operation, func(t *testing.T) {
			entered, cancelled, release := make(chan struct{}, 1), make(chan struct{}, 1), make(chan struct{})
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				io.Copy(io.Discard, r.Body)
				entered <- struct{}{}
				select {
				case <-r.Context().Done():
					cancelled <- struct{}{}
				case <-release:
				}
			}))
			defer server.Close()
			defer close(release)
			client, err := minio.New(strings.TrimPrefix(server.URL, "http://"), &minio.Options{Creds: credentials.NewStaticV4("fixture", "fixture-secret", ""), Region: "us-east-1"})
			if err != nil {
				t.Fatal(err)
			}
			store := &MinIO{client: client, bucket: "fixture", timeout: time.Second}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan error, 1)
			go func() {
				var err error
				switch operation {
				case "get":
					_, err = store.GetContext(ctx, "app", "data")
				case "head":
					_, err = store.HeadContext(ctx, "app", "data")
				case "put":
					_, err = store.PutContext(ctx, "app", "data", "text/plain", []byte("body"))
				case "delete":
					err = store.DeleteContext(ctx, "app", "data")
				case "list":
					_, err = store.ListContext(ctx, "app", "data")
				}
				done <- err
			}()
			select {
			case <-entered:
			case <-time.After(time.Second):
				t.Fatal("storage request not started")
			}
			cancel()
			select {
			case err := <-done:
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("operation did not return cancellation: %v", err)
				}
			case <-time.After(time.Second):
				t.Fatal("cancelled operation kept running")
			}
			select {
			case <-cancelled:
			case <-time.After(time.Second):
				t.Fatal("S3 request remained open after cancellation")
			}
		})
	}
}

func TestMinIOOperationDeadlineIncludesBodyRead(t *testing.T) {
	release, cancelled := make(chan struct{}), make(chan struct{}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "100")
		w.Header().Set("Last-Modified", "Wed, 21 Oct 2026 00:00:00 GMT")
		w.Header().Set("ETag", `"fixture"`)
		w.WriteHeader(200)
		if r.Method == http.MethodHead {
			return
		}
		w.Write([]byte("first"))
		w.(http.Flusher).Flush()
		select {
		case <-r.Context().Done():
			cancelled <- struct{}{}
		case <-release:
		}
	}))
	defer server.Close()
	defer close(release)
	client, err := minio.New(strings.TrimPrefix(server.URL, "http://"), &minio.Options{Creds: credentials.NewStaticV4("fixture", "fixture-secret", ""), Region: "us-east-1"})
	if err != nil {
		t.Fatal(err)
	}
	store := &MinIO{client: client, bucket: "fixture", timeout: 50 * time.Millisecond}
	// Legacy callers are finite too; the deadline spans headers and full body.
	done := make(chan error, 1)
	go func() { _, err := store.Get("app", "data"); done <- err }()
	select {
	case err := <-done:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("expected deadline, got %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("stalled body exceeded operation budget")
	}
	select {
	case <-cancelled:
	case <-time.After(time.Second):
		t.Fatal("body transport not cancelled")
	}
}

func TestMinIOOpenHasOperationDeadline(t *testing.T) {
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-release:
		}
	}))
	defer server.Close()
	defer close(release)
	_, err := Open(context.Background(), MinIOConfig{Endpoint: strings.TrimPrefix(server.URL, "http://"), AccessKey: "fixture", SecretKey: "fixture-secret", Bucket: "fixture", OperationTimeout: 50 * time.Millisecond})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("startup storage deadline: %v", err)
	}
}
