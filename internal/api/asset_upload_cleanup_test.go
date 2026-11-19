package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/clas/nanoflare/internal/nanoflare"
)

func TestAssetUploadCleanupBatchesAndRetainsLiveSessions(t *testing.T) {
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
			service, _ := managedService(t, repo, discardWriter{})
			ns, err := service.CreateDispatchNamespace("org", nanoflare.CreateDispatchNamespaceInput{Name: "cleanup"})
			must(err)
			now := time.Now().UTC()
			for index := range 4 {
				expires := now.Add(-time.Minute)
				if index == 3 {
					expires = now.Add(time.Hour)
				}
				must(repo.SaveAssetUploadSession(nanoflare.AssetUploadSession{ID: fmt.Sprint(index), OrgID: "org", NamespaceID: ns.ID, ScriptName: "alpha", Revision: 1, Manifest: map[string]nanoflare.AssetUploadEntry{}, Blobs: map[string]nanoflare.AssetUploadBlob{}, CreatedAt: now.Add(-time.Hour), ExpiresAt: expires}, 0))
			}
			count, err := repo.CountAssetUploadSessions("org", now)
			must(err)
			if count != 1 {
				t.Fatalf("live reservation count %d", count)
			}
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			if _, err = repo.PruneAssetUploadSessions(ctx, now, 2); !errors.Is(err, context.Canceled) {
				t.Fatalf("cleanup ignored cancellation: %v", err)
			}
			count, err = repo.PruneAssetUploadSessions(context.Background(), now, 2)
			must(err)
			if count != 2 {
				t.Fatalf("cleanup batch %d", count)
			}
			count, err = repo.PruneAssetUploadSessions(context.Background(), now, 2)
			must(err)
			if count != 1 {
				t.Fatalf("remaining cleanup batch %d", count)
			}
			if _, err = repo.GetAssetUploadSession("3"); err != nil {
				t.Fatal("cleanup deleted live session", err)
			}
			for _, limit := range []int{0, -1, 257} {
				if _, err = repo.PruneAssetUploadSessions(context.Background(), now, limit); err == nil {
					t.Fatal("accepted unbounded cleanup limit")
				}
			}
			live, err := repo.GetAssetUploadSession("3")
			must(err)
			live.ExpiresAt = now.Add(-time.Second)
			live.Revision++
			must(repo.SaveAssetUploadSession(live, 1))
			stop := service.StartAssetUploadCleanup(context.Background(), 10*time.Millisecond, func(err error) { t.Errorf("cleanup worker: %v", err) })
			defer stop()
			deadline := time.Now().Add(2 * time.Second)
			for {
				_, err = repo.GetAssetUploadSession("3")
				if errors.Is(err, nanoflare.ErrAssetUploadUnavailable) {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("periodic worker did not prune expired session")
				}
				time.Sleep(10 * time.Millisecond)
			}
			stop() // Stopping twice must remain safe during graceful shutdown.
		})
	}
}

func TestAssetUploadStorageQuotaUntilPruned(t *testing.T) {
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
			const org = "org"
			must(repo.CreateOrganization(nanoflare.Organization{ID: org, Name: org}))
			service, _ := managedService(t, repo, discardWriter{})
			ns, err := service.CreateDispatchNamespace(org, nanoflare.CreateDispatchNamespaceInput{Name: "storage"})
			must(err)
			now := time.Now().UTC()
			manifest := map[string]nanoflare.AssetUploadEntry{
				"/a": {Hash: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Size: nanoflare.MaxAssetUploadFileBytes},
				"/b": {Hash: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", Size: nanoflare.MaxAssetUploadFileBytes},
			}
			// Seed reservations without allocating hundreds of MiB of blob fixtures.
			for index := range nanoflare.DefaultAssetUploadStorageBytes / nanoflare.MaxAssetUploadBytes {
				must(repo.SaveAssetUploadSession(nanoflare.AssetUploadSession{
					ID: fmt.Sprint(index), OrgID: org, NamespaceID: ns.ID, ScriptName: "alpha",
					Revision: 1, Manifest: manifest, CreatedAt: now.Add(-time.Hour), ExpiresAt: now.Add(-time.Second),
				}, 0))
			}
			reserved, err := repo.ReservedAssetUploadBytes(org)
			must(err)
			if reserved != nanoflare.DefaultAssetUploadStorageBytes {
				t.Fatalf("storage reservation = %d", reserved)
			}
			must(service.SetAssetUploadSessionLimit(nanoflare.DefaultAssetUploadSessionLimit + 2))
			_, err = service.CreateAssetUploadForOrg(org, ns.ID, "alpha", "", manifest)
			if !errors.Is(err, nanoflare.ErrAssetUploadStorageExceeded) {
				t.Fatalf("expected staging storage quota error, got %v", err)
			}
			response := httptest.NewRecorder()
			cfServiceError(response, err)
			var envelope cfEnvelope
			must(json.Unmarshal(response.Body.Bytes(), &envelope))
			if response.Code != http.StatusTooManyRequests || len(envelope.Errors) != 1 || envelope.Errors[0].Code != cfAssetStorageQuotaCode {
				t.Fatalf("unexpected storage quota response: %d %s", response.Code, response.Body.String())
			}
			count, err := repo.PruneAssetUploadSessions(context.Background(), now, 1)
			must(err)
			if count != 1 {
				t.Fatalf("pruned %d sessions, want 1", count)
			}
			_, err = service.CreateAssetUploadForOrg(org, ns.ID, "alpha", "", manifest)
			must(err)
			if err := service.SetAssetUploadStorageLimit(nanoflare.MinAssetUploadReservationBytes - 1); err == nil {
				t.Fatal("accepted a budget below the minimum session reservation")
			}
			must(service.SetAssetUploadStorageLimit(nanoflare.DefaultAssetUploadStorageBytes + nanoflare.MaxAssetUploadBytes))
			_, err = service.CreateAssetUploadForOrg(org, ns.ID, "alpha", "", manifest)
			must(err)
			_, err = service.CreateAssetUploadForOrg(org, ns.ID, "alpha", "", manifest)
			if !errors.Is(err, nanoflare.ErrAssetUploadStorageExceeded) {
				t.Fatalf("expected the configured storage quota error, got %v", err)
			}
		})
	}
}

type blockingAssetCleanupRepo struct {
	nanoflare.Repository
	started chan struct{}
}

func (r blockingAssetCleanupRepo) PruneAssetUploadSessions(ctx context.Context, _ time.Time, _ int) (int, error) {
	close(r.started)
	<-ctx.Done()
	return 0, ctx.Err()
}
func TestAssetUploadCleanupStopCancelsAndJoinsDatabaseWork(t *testing.T) {
	repo := blockingAssetCleanupRepo{Repository: nanoflare.NewStore(), started: make(chan struct{})}
	service := nanoflare.NewService(repo, discardWriter{})
	stop := service.StartAssetUploadCleanup(context.Background(), time.Minute, func(err error) { t.Errorf("cancellation reported as cleanup failure: %v", err) })
	select {
	case <-repo.started:
	case <-time.After(time.Second):
		t.Fatal("cleanup did not start")
	}
	done := make(chan struct{})
	go func() { stop(); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("shutdown did not cancel/join database cleanup")
	}
}
