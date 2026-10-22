// A disposable local Workers for Platforms fixture. This is not a production
// server: control metadata and KV are in memory; SQLite and native workerd are real.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/clas/nanoflare/internal/api"
	"github.com/clas/nanoflare/internal/config"
	"github.com/clas/nanoflare/internal/database"
	"github.com/clas/nanoflare/internal/metrics"
	"github.com/clas/nanoflare/internal/nanoflare"
	"github.com/clas/nanoflare/internal/runtime"
)

type localRoutes struct{}

func (localRoutes) WriteTraefik([]nanoflare.ActiveDeployment) error { return nil }
func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}
func run() error {
	addr := flag.String("addr", "127.0.0.1:54335", "loopback control API address")
	executable := flag.String("workerd", "", "absolute path to a native workerd executable (not an npm wrapper)")
	metadata := flag.String("metadata", "", "optional output path for fixture IDs; no credentials are written")
	compatibility := flag.Bool("enable-cloudflare-compatibility", false, "enable the incomplete trusted-profile management adapter on this disposable fixture")
	flag.Parse()
	if !filepath.IsAbs(*executable) {
		return errors.New("--workerd must name an absolute native executable")
	}
	binary, err := os.Open(*executable)
	if err != nil {
		return err
	}
	header := make([]byte, 2)
	count, readErr := binary.Read(header)
	binary.Close()
	if readErr != nil {
		return readErr
	}
	if count < 2 || string(header) == "#!" {
		return errors.New("use the native workerd binary instead of a wrapper")
	}
	host, _, err := net.SplitHostPort(*addr)
	if err != nil {
		return err
	}
	if ip := net.ParseIP(host); ip == nil || !ip.IsLoopback() {
		return errors.New("local fixture must bind a loopback IP")
	}
	listener, err := net.Listen("tcp", *addr)
	if err != nil {
		return err
	}
	defer listener.Close()
	state, err := os.MkdirTemp("/tmp", "nf-wfp-demo-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(state)
	repo := nanoflare.NewStore()
	output := runtime.NewOutputBuffer()
	durations := runtime.NewDurationTelemetry()
	auth := nanoflare.NewControlAuthService(repo, "local-demo-signing-key-is-not-a-production-secret")
	auth.SetTestHashCost(4)
	session, err := auth.Signup(nanoflare.SignupInput{Email: "platform-demo@example.com", Password: "local-demo-password", OrganizationName: "Local platform demo"})
	if err != nil {
		return err
	}
	org := nanoflare.Organization{ID: "local-platform-demo", Name: "Local platform demo", UsageLevel: nanoflare.UsageLevelPaid, CreatedAt: time.Now().UTC()}
	if err = repo.CreateOrganization(org); err != nil {
		return err
	}
	if err = repo.UpsertOrganizationMembership(nanoflare.OrganizationMembership{UserID: session.User.ID, OrgID: org.ID, Role: nanoflare.RoleOwner, Scopes: nanoflare.RoleScopes(nanoflare.RoleOwner)}); err != nil {
		return err
	}
	var service *nanoflare.Service
	var server *api.Server
	privateMux := http.NewServeMux()
	privateListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	privateServer := &http.Server{Handler: privateMux, ReadHeaderTimeout: 5 * time.Second}
	defer privateServer.Close()
	writer := config.NewRuntimeWriter(filepath.Join(state, "workerd.capnp"), localRoutes{})
	writer.SetNanoflareRuntimeAddr(privateListener.Addr().String())
	manager := runtime.NewManager(writer, runtime.CommandLauncher{Executable: *executable, Output: output}, state, filepath.Join(state, "workerd.capnp"), filepath.Join(state, "sockets"), 5*time.Second, time.Second)
	defer manager.Close()
	service = nanoflare.NewServiceWithConsole(repo, manager, nil, output, metrics.NewCombinedReader(nil, durations))
	service.SetPlatformDispatchEnabled(true)
	if err = service.SetMaxCompatibilityDate("2025-12-10"); err != nil {
		return err
	}
	codec, err := nanoflare.NewSecretCodec("0123456789abcdef0123456789abcdef")
	if err != nil {
		return err
	}
	service.SetSecretCodec(codec)
	sqlite, err := database.NewSQLiteManager(filepath.Join(state, "sqlite"), nil)
	if err != nil {
		return err
	}
	service.SetDBExecutor(sqlite)
	service.SetDispatchObservationSink(func(event nanoflare.DispatchObservation) {
		durations.RecordBatch([]runtime.DurationTraceEvent{{ScriptName: event.WorkerID, EventTimestamp: float64(event.At.UnixMilli()), DurationMs: event.ElapsedMS, Outcome: event.Outcome}})
	})
	server = api.NewServerWithRuntime(service, nil, "", nil, auth, manager)
	server.SetCloudflareCompatibilityEnabled(*compatibility)
	server.SetDurationTelemetry(durations)
	server.SetMetricsToken(os.Getenv("NANOFLARE_METRICS_TOKEN"))
	privateMux.Handle("/internal/runtime/dispatch/", api.NewRuntimeDispatchServer(service, server).SetLogOutput(output))
	privateMux.Handle("/internal/runtime/logs", api.NewRuntimeLogServer(service, output))
	privateMux.Handle("/internal/runtime/durations", api.NewRuntimeDurationServer(service, durations))
	privateMux.Handle("/internal/runtime/assets/", api.NewRuntimeAssetServer(service))
	privateMux.Handle("/internal/runtime/objects/", server)
	privateMux.Handle("/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Header.Get("X-Nanoflare-Binding") {
		case "db":
			api.NewRuntimeDBServer(service).ServeHTTP(w, r)
		default:
			api.NewRuntimeKVServer(service).ServeHTTP(w, r)
		}
	}))
	go privateServer.Serve(privateListener)
	namespace, err := service.CreateDispatchNamespace(org.ID, nanoflare.CreateDispatchNamespaceInput{Name: "local-staging", CustomerMode: "platform"})
	if err != nil {
		return err
	}
	kv, err := service.CreateKVNamespace(nanoflare.CreateKVNamespaceInput{OrgID: org.ID, Name: "explicitly-shared-demo-state"})
	if err != nil {
		return err
	}
	db, err := service.CreateDatabase(nanoflare.CreateDatabaseInput{OrgID: org.ID, Name: "local-sqlite-demo"})
	if err != nil {
		return err
	}
	grants := []nanoflare.NamespaceResourceGrant{}
	for _, customer := range []string{"customer-a", "customer-b"} {
		grants = append(grants, nanoflare.NamespaceResourceGrant{Kind: "kv", ResourceID: kv.ID, CustomerID: customer}, nanoflare.NamespaceResourceGrant{Kind: "db", ResourceID: db.ID, CustomerID: customer})
	}
	namespace, err = service.SetNamespaceGrants(org.ID, namespace.ID, nanoflare.NamespaceGrantsInput{ExpectedRevision: 1, Grants: grants})
	if err != nil {
		return err
	}
	workers := map[string]string{}
	for index, name := range []string{"alpha", "beta"} {
		customer := []string{"customer-a", "customer-b"}[index]
		worker, e := service.CreateNamespaceScript(org.ID, namespace.ID, nanoflare.CreateNamespaceScriptInput{ScriptName: name, CustomerID: customer, Execution: "dynamic"})
		if e != nil {
			return e
		}
		workers[name] = worker.ID
		if e = service.PutSecret(worker.ID, "TOKEN", name+"-local-demo-secret"); e != nil {
			return e
		}
		_, e = service.Deploy(worker.ID, nanoflare.DeployInput{IdempotencyKey: name + "-initial", Format: "modules", Entrypoint: "worker.js", CompatibilityDate: "2025-12-10", KVNamespaces: []nanoflare.KVBinding{{Binding: "STATE", ID: kv.ID}}, Databases: []nanoflare.DatabaseBinding{{Binding: "DB", DatabaseID: db.ID}}, Files: []nanoflare.WorkerFile{{Path: "worker.js", Content: `export default {async fetch(request,env,ctx){
 console.log("Customer request",ctx.props.customer);
 await env.STATE.put(ctx.props.customer,"seen");
 const answer=await env.DB.prepare("SELECT 42 AS answer").first("answer");
 return Response.json({customer:ctx.props.customer,answer,state:await env.STATE.get(ctx.props.customer),platform:await ctx.props.PLATFORM.identify()});
}}`}}})
		if e != nil {
			return e
		}
	}
	platform, err := service.CreateApp(nanoflare.CreateAppInput{OrgID: org.ID, Name: "local-platform", Hostname: "platform.localdemo.test"})
	if err != nil {
		return err
	}
	workers["platform"] = platform.ID
	_, err = service.Deploy(platform.ID, nanoflare.DeployInput{IdempotencyKey: "platform-initial", Format: "modules", Entrypoint: "platform.js", CompatibilityDate: "2025-12-10", DispatchNamespaces: []nanoflare.DispatchBinding{{Binding: "CUSTOMERS", NamespaceID: namespace.ID}}, Files: []nanoflare.WorkerFile{{Path: "platform.js", Content: `import {WorkerEntrypoint} from "cloudflare:workers";
 export class Platform extends WorkerEntrypoint{identify(){return "local platform";}}
 export default {async fetch(request,env,ctx){
 if(request.headers.get("Authorization")!=="Bearer local-demo")return new Response("denied",{status:403});
 const script=new URL(request.url).pathname.split("/").filter(Boolean)[0]||"alpha";
 try{return await env.CUSTOMERS.get(script,{props:{customer:script,PLATFORM:ctx.exports.Platform({props:{}})}}).fetch(request)}catch(error){return Response.json({error:error.message},{status:error.status||500})}
 }};`}}})
	if err != nil {
		return err
	}
	apiURL := "http://" + listener.Addr().String()
	info := map[string]any{"api_url": apiURL, "organization_id": org.ID, "namespace_id": namespace.ID, "workers": workers, "kv_namespace_id": kv.ID, "database_id": db.ID, "resources": map[string]string{"runtime": "native workerd", "control_metadata": "ephemeral in-memory repository", "kv": "emulated in-memory store", "database": "real local SQLite file", "objects": "not configured", "cloudflare": "not used"}, "dispatch_url": apiURL + "/internal/http/workers/" + platform.ID + "/alpha"}
	if *metadata != "" {
		encoded, e := json.MarshalIndent(info, "", "  ")
		if e != nil {
			return e
		}
		if e = os.WriteFile(*metadata, append(encoded, '\n'), 0600); e != nil {
			return e
		}
	}
	fmt.Printf("Local fixture listening on %s; all state is disposable.\n", apiURL)
	fmt.Printf("Sign in as platform-demo@example.com using the local-demo-password fixture password.\n")
	fmt.Printf("Dispatch endpoint: %s/internal/http/workers/%s/alpha (Authorization: Bearer local-demo).\n", apiURL, platform.ID)
	publicServer := &http.Server{Handler: server, ReadHeaderTimeout: 5 * time.Second}
	shutdown, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	stopAssetCleanup := service.StartAssetUploadCleanup(shutdown, time.Minute, func(err error) { fmt.Printf("asset staging cleanup: %v\n", err) })
	defer stopAssetCleanup()
	go func() {
		<-shutdown.Done()
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		publicServer.Shutdown(ctx)
	}()
	err = publicServer.Serve(listener)
	if errors.Is(err, http.ErrServerClosed) || strings.Contains(fmt.Sprint(err), "use of closed network connection") {
		return nil
	}
	return err
}
