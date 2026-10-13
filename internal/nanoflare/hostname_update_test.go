package nanoflare

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestHostnameValidationSharedByCreateAndUpdate(t *testing.T) {
	invalid := []string{"", " ", "https://site.example.com", "site.example.com/path", "site.example.com:443", "*.example.com", "127.0.0.1", "::1", "bad name.example.com", "a..example.com", ".example.com", "-a.example.com", "a-.example.com", "a_.example.com", "é.example.com", "K.example.com", "a.example.com?x", "a.example.com#x", "a.example.com..", strings.Repeat("a", 64) + ".example.com", strings.Repeat("a.", 127) + "a"}
	service := NewService(NewStore(), &recordingWriter{})
	app, err := service.CreateApp(CreateAppInput{Name: "Site", Hostname: "old.example.com"})
	if err != nil {
		t.Fatal(err)
	}
	for _, hostname := range invalid {
		t.Run(hostname, func(t *testing.T) {
			if _, err := service.UpdateApp(app.ID, UpdateAppInput{Hostname: &hostname}); !errors.Is(err, ErrInvalidAppUpdate) {
				t.Fatalf("update error = %v", err)
			}
			// Empty creation inputs retain their existing auto-generation behavior.
			if strings.TrimSpace(hostname) != "" {
				if _, err := service.CreateApp(CreateAppInput{Name: "Invalid", Hostname: hostname}); err == nil {
					t.Fatal("creation accepted invalid hostname")
				}
			}
			saved, err := service.store.GetApp(app.ID)
			if err != nil || saved.Hostname != app.Hostname {
				t.Fatalf("invalid update changed worker: %#v, %v", saved, err)
			}
		})
	}
	for _, hostname := range []string{" New-Site.Example.COM. ", "xn--bcher-kva.example.com", strings.Repeat("a", 63) + ".example.com", strings.Repeat("a", 63) + "." + strings.Repeat("b", 63) + "." + strings.Repeat("c", 63) + "." + strings.Repeat("d", 61), "localhost"} {
		updated, err := service.UpdateApp(app.ID, UpdateAppInput{Hostname: &hostname})
		if err != nil {
			t.Fatal(err)
		}
		if updated.Hostname != strings.TrimSuffix(strings.ToLower(strings.TrimSpace(hostname)), ".") {
			t.Fatalf("hostname = %q", updated.Hostname)
		}
	}
}

func TestUpdateAppInputRejectsNullAndUnknownFields(t *testing.T) {
	for _, body := range []string{`{"hostname":null}`, `{"hostname":42}`, `{"hostname":"site.example.com","unexpected":true}`} {
		var input UpdateAppInput
		if err := json.Unmarshal([]byte(body), &input); err == nil {
			t.Fatalf("accepted %s", body)
		}
	}
	var input UpdateAppInput
	if err := json.Unmarshal([]byte(`{"auth":null}`), &input); err != nil || input.Hostname != nil {
		t.Fatalf("omission: %#v, %v", input, err)
	}
}

type hostnameWriter struct {
	active []ActiveDeployment
	err    error
	writes int
}

func (w *hostnameWriter) Write(active []ActiveDeployment) error {
	w.writes++
	if w.err != nil {
		return w.err
	}
	w.active = append([]ActiveDeployment(nil), active...)
	return nil
}

func TestHostnameUpdatePreservesWorkerAndRecoversRefresh(t *testing.T) {
	store := NewStore()
	writer := &hostnameWriter{}
	service := NewService(store, writer)
	if err := store.CreateOrganization(Organization{ID: "org-a", Name: "Sites"}); err != nil {
		t.Fatal(err)
	}
	codec, err := NewSecretCodec(strings.Repeat("k", 32))
	if err != nil {
		t.Fatal(err)
	}
	service.SetSecretCodec(codec)
	app, err := service.CreateApp(CreateAppInput{Name: "Site", OrgID: "org-a", Hostname: "old.example.com", ExternalID: "external-site", OAuthClientID: "client", CreatedBy: "creator"})
	if err != nil {
		t.Fatal(err)
	}
	deployments := []Deployment{
		{ID: "deployment-a", AppID: app.ID, Files: []WorkerFile{{Path: "worker.js", Content: "export default {}"}}, Entrypoint: "worker.js", Port: 21001, CreatedAt: time.Now(), KVNamespaces: []KVBinding{{Binding: "KV", ID: "namespace"}}, Databases: []DatabaseBinding{{Binding: "DB", DatabaseID: "db"}}, Services: []ServiceBinding{{Binding: "API", Service: "backend"}}, ObjectStorageBuckets: []ObjectStorageBucketBinding{{Binding: "BUCKET", BucketID: "bucket"}}},
		{ID: "deployment-b", AppID: app.ID, Files: []WorkerFile{{Path: "worker.js", Content: "export default {}"}}, Entrypoint: "worker.js", Port: 21002, CreatedAt: time.Now().Add(time.Second)},
	}
	for _, deployment := range deployments {
		if err := store.Activate(deployment); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.SetActiveTraffic(app.ID, []DeploymentTraffic{{ID: "deployment-a", TrafficPercent: 30}, {ID: "deployment-b", TrafficPercent: 70}}); err != nil {
		t.Fatal(err)
	}
	nonce, ciphertext, err := codec.Encrypt("secret-value")
	if err != nil {
		t.Fatal(err)
	}
	secret := SecretRecord{Secret: Secret{Name: "TOKEN"}, Nonce: nonce, Ciphertext: ciphertext}
	if err := store.PutSecret(app.ID, secret); err != nil {
		t.Fatal(err)
	}
	before, err := store.ActiveDeployments()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.runtimeDeploymentsForApp(app.ID); err != nil {
		t.Fatal(err)
	}
	hostname := " New.Example.COM. "
	input := UpdateAppInput{Hostname: &hostname, Auth: &AuthConfig{ProtectedRoutes: []string{" /admin/* "}}}
	writer.err = errors.New("publisher unavailable")
	if _, err := service.UpdateAppForOrg("org-a", app.ID, input); err == nil || !strings.Contains(err.Error(), "saved but routing refresh failed") {
		t.Fatalf("refresh error = %v", err)
	}
	saved, err := store.GetApp(app.ID)
	if err != nil {
		t.Fatal(err)
	}
	if saved.Hostname != "new.example.com" || saved.Auth.ProtectedRoutes[0] != "/admin/*" {
		t.Fatalf("saved = %#v", saved)
	}
	expected := app
	expected.Hostname = saved.Hostname
	expected.Auth = saved.Auth
	if !reflect.DeepEqual(saved, expected) {
		t.Fatalf("worker metadata changed: got %#v, want %#v", saved, expected)
	}
	detail, err := service.WorkerDetailForOrg("org-a", app.ID)
	if err != nil || detail.App.Hostname != saved.Hostname || len(detail.Secrets) != 1 {
		t.Fatalf("GET reconciliation = %#v, %v", detail, err)
	}
	secrets, err := store.ListSecrets(app.ID)
	if err != nil || !reflect.DeepEqual(secrets, []SecretRecord{secret}) {
		t.Fatalf("secrets changed: %#v, %v", secrets, err)
	}
	writer.err = nil
	hostname = saved.Hostname
	if _, err := service.UpdateAppForOrg("org-a", app.ID, UpdateAppInput{Hostname: &hostname}); err != nil {
		t.Fatal(err)
	}
	if writer.writes != 2 {
		t.Fatalf("writes = %d", writer.writes)
	}
	for i, active := range writer.active {
		if active.App.Hostname != saved.Hostname || active.TrafficPercent != before[i].TrafficPercent || !reflect.DeepEqual(active.Deployment, before[i].Deployment) {
			t.Fatalf("deployment changed: %#v", active)
		}
	}
	refreshed, err := service.runtimeDeploymentsForApp(app.ID)
	if err != nil || refreshed[0].App.Hostname != saved.Hostname {
		t.Fatalf("runtime cache = %#v, %v", refreshed, err)
	}
}

func TestHostnameUpdateConflictAndCombinedValidationAreAtomic(t *testing.T) {
	store := NewStore()
	for _, id := range []string{"a", "b"} {
		if err := store.CreateOrganization(Organization{ID: id, Name: id}); err != nil {
			t.Fatal(err)
		}
	}
	service := NewService(store, &recordingWriter{})
	app, err := service.CreateApp(CreateAppInput{Name: "A", OrgID: "a", Hostname: "a.example.com"})
	if err != nil {
		t.Fatal(err)
	}
	other, err := service.CreateApp(CreateAppInput{Name: "B", OrgID: "b", Hostname: "b.example.com"})
	if err != nil {
		t.Fatal(err)
	}
	hostname := other.Hostname
	if _, err := service.UpdateAppForOrg("a", app.ID, UpdateAppInput{Hostname: &hostname, Auth: &AuthConfig{ProtectedRoutes: []string{"/admin"}}}); !errors.Is(err, ErrAppExists) {
		t.Fatalf("conflict = %v", err)
	}
	hostname = "new.example.com"
	if _, err := service.UpdateAppForOrg("a", app.ID, UpdateAppInput{Hostname: &hostname, Auth: &AuthConfig{ProtectedRoutes: []string{"invalid"}}}); !errors.Is(err, ErrInvalidAppUpdate) {
		t.Fatalf("validation = %v", err)
	}
	if _, err := service.UpdateAppForOrg("b", app.ID, UpdateAppInput{Hostname: &hostname}); !errors.Is(err, ErrAppNotFound) {
		t.Fatalf("organization = %v", err)
	}
	saved, err := service.store.GetApp(app.ID)
	if err != nil || !reflect.DeepEqual(saved, app) {
		t.Fatalf("failed request changed worker: %#v, %v", saved, err)
	}
	if _, err := service.UpdateApp(app.ID, UpdateAppInput{Auth: &AuthConfig{ProtectedRoutes: []string{"/private"}}}); err != nil {
		t.Fatal(err)
	}
	saved, _ = service.store.GetApp(app.ID)
	if saved.Hostname != app.Hostname {
		t.Fatal("auth-only update changed hostname")
	}
}

func TestConcurrentHostnameAndAuthUpdatesPreserveOmittedFields(t *testing.T) {
	service := NewService(NewStore(), &hostnameWriter{})
	app, err := service.CreateApp(CreateAppInput{Name: "Site", Hostname: "old.example.com"})
	if err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	var wg sync.WaitGroup
	for _, input := range []UpdateAppInput{{Hostname: ptrHostname("new.example.com")}, {Auth: &AuthConfig{ProtectedRoutes: []string{"/private"}}}} {
		wg.Add(1)
		go func(input UpdateAppInput) {
			defer wg.Done()
			<-start
			if _, err := service.UpdateApp(app.ID, input); err != nil {
				t.Error(err)
			}
		}(input)
	}
	close(start)
	wg.Wait()
	saved, err := service.store.GetApp(app.ID)
	if err != nil || saved.Hostname != "new.example.com" || len(saved.Auth.ProtectedRoutes) != 1 {
		t.Fatalf("concurrent update = %#v, %v", saved, err)
	}
}
func ptrHostname(hostname string) *string { return &hostname }

type metadataOnlyRepo struct{ *Store }

func (r *metadataOnlyRepo) ActiveDeployments() ([]ActiveDeployment, error) {
	return nil, errors.New("unrelated deployment unavailable")
}
func TestWorkerDetailDoesNotHydrateDeploymentBundles(t *testing.T) {
	repo := &metadataOnlyRepo{NewStore()}
	service := NewService(repo, &recordingWriter{})
	app, err := service.CreateApp(CreateAppInput{Name: "Site", Hostname: "old.example.com"})
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.Activate(Deployment{ID: "deployment", AppID: app.ID, ObjectKey: "unavailable-bundle", BundleSize: 42, CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	hostname := "new.example.com"
	if _, err := service.UpdateApp(app.ID, UpdateAppInput{Hostname: &hostname}); err == nil {
		t.Fatal("expected snapshot failure after save")
	}
	detail, err := service.WorkerDetail(app.ID)
	if err != nil || detail.App.Hostname != hostname || detail.Deployment == nil || detail.Deployment.BundleSize != 42 {
		t.Fatalf("detail = %#v, %v", detail, err)
	}
}

func TestStoreConcurrentHostnameClaimAndOmittedFields(t *testing.T) {
	store := NewStore()
	for _, id := range []string{"a", "b"} {
		if err := store.CreateApp(App{ID: id, Hostname: id + ".example.com"}); err != nil {
			t.Fatal(err)
		}
	}
	hostname := "claimed.example.com"
	start := make(chan struct{})
	results := make(chan error, 2)
	for _, id := range []string{"a", "b"} {
		go func(id string) {
			<-start
			_, err := store.PatchApp(id, UpdateAppInput{Hostname: &hostname})
			results <- err
		}(id)
	}
	close(start)
	first, second := <-results, <-results
	if !((first == nil && errors.Is(second, ErrAppExists)) || (second == nil && errors.Is(first, ErrAppExists))) {
		t.Fatalf("concurrent claim errors=%v, %v", first, second)
	}
	auth := AuthConfig{ProtectedRoutes: []string{"/private"}}
	start = make(chan struct{})
	var wg sync.WaitGroup
	for _, input := range []UpdateAppInput{{Hostname: ptrHostname("final.example.com")}, {Auth: &auth}} {
		wg.Add(1)
		go func(input UpdateAppInput) {
			defer wg.Done()
			<-start
			if _, err := store.PatchApp("a", input); err != nil {
				t.Error(err)
			}
		}(input)
	}
	close(start)
	wg.Wait()
	saved, err := store.GetApp("a")
	if err != nil || saved.Hostname != "final.example.com" || !reflect.DeepEqual(saved.Auth, auth) {
		t.Fatalf("patch lost field=%#v, %v", saved, err)
	}
}
