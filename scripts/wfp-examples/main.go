// Runs pinned upstream bundles on a real local Nanoflare runtime. All listeners
// are loopback-only. This is a trusted example environment, not public hosting.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/clas/nanoflare/internal/api"
	"github.com/clas/nanoflare/internal/config"
	"github.com/clas/nanoflare/internal/database"
	"github.com/clas/nanoflare/internal/egress"
	"github.com/clas/nanoflare/internal/nanoflare"
	"github.com/clas/nanoflare/internal/objects"
	"github.com/clas/nanoflare/internal/runtime"
)

type routes struct{}

func (routes) WriteTraefik([]nanoflare.ActiveDeployment) error { return nil }
func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}
func run() error {
	workerd := flag.String("workerd", "", "native workerd binary")
	state := flag.String("state", "", "persistent local state directory")
	publisher := flag.String("publisher", "", "upstream publisher bundle")
	sites := flag.String("sites", "", "upstream sites bundle")
	portBase := flag.Int("port-base", 54337, "first of three consecutive loopback ports")
	flag.Parse()
	if *portBase < 1024 || *portBase > 65533 {
		return fmt.Errorf("port-base must be between 1024 and 65533")
	}
	if !filepath.IsAbs(*state) || !filepath.IsAbs(*workerd) {
		return fmt.Errorf("absolute --state and --workerd are required")
	}
	if err := os.MkdirAll(*state, 0700); err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	repo, err := database.Open(ctx, os.Getenv("NANOFLARE_EXAMPLES_DATABASE_URL"))
	if err != nil {
		return err
	}
	defer repo.Close()
	objectStore, err := objects.Open(ctx, objects.MinIOConfig{Endpoint: "127.0.0.1:9000", AccessKey: "nanoflare", SecretKey: "nanoflare-development", Bucket: "nanoflare-upstream-examples"})
	if err != nil {
		return err
	}
	auth := nanoflare.NewControlAuthService(repo, "local-upstream-example-session-key-not-for-production")
	session, err := auth.Login(nanoflare.LoginInput{Email: "examples@nanoflare.local", Password: "local-examples-password"})
	if err != nil {
		session, err = auth.Signup(nanoflare.SignupInput{Email: "examples@nanoflare.local", Password: "local-examples-password", OrganizationName: "Upstream examples"})
	}
	if err != nil {
		return err
	}
	orgID := "upstream-examples"
	if _, err = repo.GetOrganization(orgID); err != nil {
		if err = repo.CreateOrganization(nanoflare.Organization{ID: orgID, Name: "Upstream examples", UsageLevel: nanoflare.UsageLevelPaid, CreatedAt: time.Now().UTC()}); err != nil {
			return err
		}
	}
	if err = repo.UpsertOrganizationMembership(nanoflare.OrganizationMembership{UserID: session.User.ID, OrgID: orgID, Role: nanoflare.RoleOwner, Scopes: nanoflare.RoleScopes(nanoflare.RoleOwner)}); err != nil {
		return err
	}
	private, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}

	writer := config.NewRuntimeWriter(filepath.Join(*state, "workerd.capnp"), routes{})
	writer.SetNanoflareRuntimeAddr(private.Addr().String())
	output := runtime.NewOutputBuffer()
	if os.Getenv("NANOFLARE_EXAMPLES_DEBUG") == "1" {
		go func() {
			seen := map[string]int{}
			for {
				select {
				case <-ctx.Done():
					return
				case <-time.After(time.Second):
					apps, _ := repo.ListApps()
					appIDs := []string{""}
					for _, app := range apps {
						appIDs = append(appIDs, app.ID)
					}
					for _, id := range appIDs {
						lines := output.Output(id)
						if len(lines) > seen[id] {
							for _, line := range lines[seen[id]:] {
								log.Print(line.Message)
							}
							seen[id] = len(lines)
						}
					}
				}
			}
		}()
	}
	socketDir, err := os.MkdirTemp("/tmp", "nf-examples-sockets-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(socketDir)
	manager := runtime.NewManager(writer, runtime.CommandLauncher{Executable: *workerd, Output: output}, *state, filepath.Join(*state, "workerd.capnp"), socketDir, 10*time.Second, time.Second)
	defer manager.Close()
	service := nanoflare.NewServiceWithConsole(repo, manager, objectStore, output, nil)
	service.SetPlatformDispatchEnabled(true)
	if err = service.SetMaxCompatibilityDate("2026-10-22"); err != nil {
		return err
	}
	codec, err := nanoflare.NewSecretCodec("0123456789abcdef0123456789abcdef")
	if err != nil {
		return err
	}
	service.SetSecretCodec(codec)
	sqlite, err := database.NewSQLiteManager(filepath.Join(*state, "sqlite"), nil)
	if err != nil {
		return err
	}
	service.SetDBExecutor(sqlite)
	server := api.NewServerWithRuntime(service, nil, "", nil, auth, manager)
	server.SetCloudflareCompatibilityEnabled(true)
	stopCleanup := service.StartAssetUploadCleanup(ctx, time.Minute, func(err error) { log.Printf("asset staging cleanup: %v", err) })
	defer stopCleanup()
	mux := http.NewServeMux()
	mux.Handle("/internal/runtime/dispatch/", api.NewRuntimeDispatchServer(service, server).SetLogOutput(output))
	mux.Handle("/internal/runtime/logs", api.NewRuntimeLogServer(service, output))
	mux.Handle("/internal/runtime/access", api.NewRuntimeAccessServer(service, auth))
	mux.Handle("/internal/runtime/durations", api.NewRuntimeDurationServer(service, runtime.NewDurationTelemetry()))
	mux.Handle("/internal/runtime/assets/", api.NewRuntimeAssetServer(service))
	mux.Handle("/internal/runtime/objects/", server)
	mux.Handle("/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Header.Get("X-Nanoflare-Binding") {
		case "db":
			api.NewRuntimeDBServer(service).ServeHTTP(w, r)
		case "assets":
			api.NewRuntimeAssetServer(service).ServeHTTP(w, r)
		default:
			api.NewRuntimeKVServer(service).ServeHTTP(w, r)
		}
	}))
	privateServer := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	defer privateServer.Close()
	go privateServer.Serve(private)
	gateway := api.NewCloudflareCompatibilityGateway(server, orgID)
	egressServer, err := egress.NewLocalGateway("127.0.0.1:0", gateway)
	if err != nil {
		return err
	}
	if err = egressServer.Start(); err != nil {
		return err
	}
	defer egressServer.Close(context.Background())
	writer.SetWorkerdEgressAddr(egressServer.Addr())
	// Reconcile persisted customer scripts before deploying the platform bundles.
	if err = service.RecoverPublications(); err != nil {
		return err
	}
	if err = service.ReconcileRuntime(); err != nil {
		return err
	}
	var db nanoflare.Database
	databases, err := service.ListDatabasesForOrg(orgID)
	if err != nil {
		return err
	}
	for _, candidate := range databases {
		if candidate.Name == "internal-sites-platform" {
			db = candidate
		}
	}
	if db.ID == "" {
		db, err = service.CreateDatabase(nanoflare.CreateDatabaseInput{OrgID: orgID, Name: "internal-sites-platform"})
		if err != nil {
			return err
		}
	}
	ids := map[string]string{}
	for _, example := range []struct {
		name, namespace, binding, bundle string
		port                             int
		isSites                          bool
	}{
		{"worker-publisher-template", "my-dispatch-namespace", "DISPATCHER", *publisher, *portBase, false},
		{"internal-sites-template", "internal-sites", "dispatcher", *sites, *portBase + 1, true},
	} {
		var ns nanoflare.DispatchNamespace
		namespaces, e := service.DispatchNamespacesForOrg(orgID)
		if e != nil {
			return e
		}
		for _, candidate := range namespaces {
			if candidate.Name == example.namespace {
				ns = candidate
			}
		}
		if ns.ID == "" {
			ns, err = service.CreateDispatchNamespace(orgID, nanoflare.CreateDispatchNamespaceInput{Name: example.namespace})
			if err != nil {
				return err
			}
		}
		apps, err := service.ListAppsForOrg(orgID)
		if err != nil {
			return err
		}
		var app nanoflare.App
		for _, candidate := range apps {
			if candidate.Name == example.name {
				app = candidate
			}
		}
		if app.ID == "" {
			app, err = service.CreateApp(nanoflare.CreateAppInput{OrgID: orgID, Name: example.name, Hostname: example.name + ".localhost"})
			if err != nil {
				return err
			}
		}
		tokenName := "CLOUDFLARE_API_TOKEN"
		if example.isSites {
			tokenName = "DISPATCH_NAMESPACE_API_TOKEN"
		}
		if err = service.PutSecret(app.ID, tokenName, session.Token); err != nil {
			return err
		}
		source, err := os.ReadFile(example.bundle)
		if err != nil {
			return err
		}
		vars := map[string]json.RawMessage{"CLOUDFLARE_ACCOUNT_ID": jsonString(orgID), "READONLY": jsonString("false")}
		bindings := []nanoflare.DatabaseBinding{}
		if example.isSites {
			vars = map[string]json.RawMessage{"ACCOUNT_ID": jsonString(orgID), "DISPATCH_NAMESPACE_NAME": jsonString(example.namespace), "SITE_DOMAIN": jsonString("internal-company.com"), "DEPLOY_PATH": jsonString("/deploy")}
			bindings = append(bindings, nanoflare.DatabaseBinding{Binding: "DB", DatabaseID: db.ID})
		}
		_, err = service.Deploy(app.ID, nanoflare.DeployInput{Format: "modules", Entrypoint: "index.js", CompatibilityDate: "2025-10-08", CompatibilityFlags: []string{"nodejs_compat"}, Vars: vars, Databases: bindings, DispatchNamespaces: []nanoflare.DispatchBinding{{Binding: example.binding, NamespaceID: ns.ID}}, Files: []nanoflare.WorkerFile{{Path: "index.js", Content: string(source)}}})
		if err != nil {
			return fmt.Errorf("deploy %s: %w", example.name, err)
		}
		ids[example.name] = app.ID
		target := app.ID
		isSites := example.isSites
		ingress := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Sandboxed path previews have opaque origins. Their asset requests
			// need SameSite=None cookies; reject foreign-origin browser mutations.
			if r.Method != "GET" && r.Method != "HEAD" && r.Header.Get("Origin") != "" && r.Header.Get("Origin") != "http://"+r.Host {
				http.Error(w, "origin denied", 403)
				return
			}
			if r.URL.Path == "/__nanoflare/login" {
				if r.Method == "POST" {
					if err := r.ParseForm(); err != nil {
						http.Error(w, "invalid form", 400)
						return
					}
					login, e := auth.Login(nanoflare.LoginInput{Email: r.Form.Get("email"), Password: r.Form.Get("password")})
					if e != nil {
						http.Error(w, "invalid credentials", 401)
						return
					}
					if _, e = auth.Membership(login.User.ID, orgID); e != nil {
						http.Error(w, "access denied", 403)
						return
					}
					http.SetCookie(w, &http.Cookie{Name: "nf_examples_session", Value: login.Token, Path: "/", HttpOnly: true, SameSite: http.SameSiteNoneMode, Secure: true, MaxAge: 86400})
					http.Redirect(w, r, "/", 303)
					return
				}
				w.Header().Set("Content-Type", "text/html; charset=utf-8")
				fmt.Fprint(w, `<!doctype html><title>Nanoflare example sign in</title><h1>Sign in to Nanoflare examples</h1><form method="post"><label>Email <input name="email" type="email" required></label><label>Password <input name="password" type="password" required></label><button>Sign in</button></form>`)
				return
			}
			request := r.Clone(r.Context())
			request.Header = r.Header.Clone()
			request.Header.Del("X-Nanoflare-User-JWT")
			request.Header.Del("X-Nanoflare-Dispatch-Root")
			if isSites {
				cookie, e := r.Cookie("nf_examples_session")
				if e != nil {
					http.Redirect(w, r, "/__nanoflare/login", 303)
					return
				}
				identity, e := auth.Me(cookie.Value)
				if e != nil {
					http.Redirect(w, r, "/__nanoflare/login", 303)
					return
				}
				if _, e = auth.Membership(identity.User.ID, orgID); e != nil {
					http.Error(w, "access denied", 403)
					return
				}
				request.Header.Set("X-Nanoflare-User-JWT", cookie.Value)
			}
			request.URL.Path = "/internal/http/workers/" + target + r.URL.Path
			request.URL.RawPath = ""
			server.ServeHTTP(w, request)
		})
		listener, e := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", example.port))
		if e != nil {
			return e
		}
		public := &http.Server{Handler: ingress, ReadHeaderTimeout: 5 * time.Second}
		defer public.Close()
		go public.Serve(listener)
		fmt.Printf("%s: http://127.0.0.1:%d\n", example.name, example.port)
	}
	// Explicit host-side base URL for SDK/CLI tests; this listener never forwards externally.
	control, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", *portBase+2))
	if err != nil {
		return err
	}
	controlServer := &http.Server{Handler: gateway, ReadHeaderTimeout: 5 * time.Second}
	defer controlServer.Close()
	go controlServer.Serve(control)
	data, _ := json.MarshalIndent(map[string]any{"organization": orgID, "workers": ids, "api_url": fmt.Sprintf("http://127.0.0.1:%d/client/v4", *portBase+2), "token": session.Token, "database_id": db.ID}, "", "  ")
	if err = os.WriteFile(filepath.Join(*state, "credentials.json"), append(data, '\n'), 0600); err != nil {
		return err
	}
	fmt.Println("Internal Sites sign in: examples@nanoflare.local / local-examples-password (local fixture only)")
	<-ctx.Done()
	return nil
}
func jsonString(value string) json.RawMessage { data, _ := json.Marshal(value); return data }
