package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/clas/nanoflare/internal/nanoflare"
)

type stalledContextObjects struct {
	*apiObjectStore
	entered chan struct{}
	writes  atomic.Int32
}

func (s *stalledContextObjects) GetContext(ctx context.Context, app, path string) (nanoflare.ObjectBody, error) {
	s.entered <- struct{}{}
	<-ctx.Done()
	return nanoflare.ObjectBody{}, ctx.Err()
}
func (s *stalledContextObjects) HeadContext(ctx context.Context, app, path string) (nanoflare.ObjectInfo, error) {
	s.entered <- struct{}{}
	<-ctx.Done()
	return nanoflare.ObjectInfo{}, ctx.Err()
}
func (s *stalledContextObjects) PutContext(ctx context.Context, app, path, kind string, body []byte) (nanoflare.ObjectInfo, error) {
	s.writes.Add(1)
	return nanoflare.ObjectInfo{}, ctx.Err()
}
func (s *stalledContextObjects) DeleteContext(ctx context.Context, app, path string) error {
	s.writes.Add(1)
	return ctx.Err()
}
func (s *stalledContextObjects) ListContext(ctx context.Context, app, path string) ([]nanoflare.ObjectInfo, error) {
	return nil, ctx.Err()
}

func TestRuntimeStorageCancellationReachesBackend(t *testing.T) {
	for _, backend := range []string{"memory", "postgres"} {
		t.Run(backend, func(t *testing.T) {
			var repo nanoflare.Repository = nanoflare.NewStore()
			if backend == "postgres" {
				repo = runtimePostgres(t)
			}
			must := func(err error) {
				t.Helper()
				if err != nil {
					t.Fatal(err)
				}
			}
			must(repo.CreateOrganization(nanoflare.Organization{ID: "org", Name: "org"}))
			app := nanoflare.App{ID: "app", OrgID: "org", Name: "app", Hostname: "storage.example.test", RuntimeToken: "fixture-root"}
			must(repo.CreateApp(app))
			must(repo.CreateObjectStorageBucket(nanoflare.ObjectStorageBucket{ID: "bucket", OrgID: "org", Name: "bucket"}))
			deployment := nanoflare.Deployment{ID: "v1", AppID: app.ID, Port: 9001, ObjectStorageBuckets: []nanoflare.ObjectStorageBucketBinding{{Binding: "BUCKET", BucketID: "bucket"}}, Assets: []nanoflare.AssetFile{{Path: "proof.txt", ObjectKey: "proof"}}}
			must(repo.Activate(deployment))
			item := nanoflare.ActiveDeployment{App: app, Deployment: deployment}
			for _, operation := range []string{"asset", "get", "head", "put", "delete"} {
				t.Run(operation, func(t *testing.T) {
					store := &stalledContextObjects{apiObjectStore: newAPIObjectStore(), entered: make(chan struct{}, 1)}
					service := nanoflare.NewServiceWithObjects(repo, discardWriter{}, store)
					server := NewServer(service)
					var handler http.Handler = server
					method, path, kind, resource := "GET", "/internal/runtime/objects/data", nanoflare.RuntimeObjects, "bucket"
					switch operation {
					case "asset":
						handler = NewRuntimeAssetServer(service)
						path = "/internal/runtime/assets/proof.txt"
						kind = nanoflare.RuntimeAssets
						resource = deployment.ID
					case "head":
						method = "HEAD"
					case "put":
						method = "PUT"
					case "delete":
						method = "DELETE"
					}
					ctx, cancel := context.WithCancel(context.Background())
					defer cancel()
					request := httptest.NewRequest(method, path, strings.NewReader("body")).WithContext(ctx)
					request.Header.Set("Authorization", "Bearer "+nanoflare.RuntimeCapability(item, kind, resource))
					request.Header.Set("X-Nanoflare-Object-Bucket-ID", "bucket")
					request.Header.Set("X-Nanoflare-Deployment-ID", deployment.ID)
					response := httptest.NewRecorder()
					done := make(chan struct{})
					go func() { handler.ServeHTTP(response, request); close(done) }()
					select {
					case <-store.entered:
					case <-time.After(time.Second):
						t.Fatal("runtime did not reach context-aware store")
					}
					cancel()
					select {
					case <-done:
					case <-time.After(time.Second):
						t.Fatal("runtime handler ignored cancellation")
					}
					if response.Code == 200 || response.Code == 204 || store.writes.Load() != 0 {
						t.Fatalf("cancelled storage operation succeeded or started a late write: %d", response.Code)
					}
				})
			}
		})
	}
}
