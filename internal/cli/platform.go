package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/clas/nanoflare/internal/nanoflare"
	"github.com/spf13/cobra"
)

// platform commands use immutable IDs and the same organization authorization
// as ordinary deploy. JSON output is suitable for scripting without hiding errors.
func (r *Runner) platformCommand() *cobra.Command {
	root := &cobra.Command{Use: "platform", Short: "Manage private Workers and dispatch namespaces"}
	showHelpWhenEmpty(root)
	root.PersistentFlags().String("api-url", envOrDefault("NANOFLARED_URL", defaultAPIURL), "Nanoflare API URL")
	root.PersistentFlags().String("org", "", "Organization ID (defaults to the active organization)")
	namespace := &cobra.Command{Use: "namespace", Short: "Manage dispatch namespaces"}
	showHelpWhenEmpty(namespace)
	namespace.AddCommand(r.platformJSON("list", "List dispatch namespaces", http.MethodGet, func([]string) string { return "/dispatch-namespaces" }, nil))
	create := r.platformJSON("create <name>", "Create a trusted dispatch namespace", http.MethodPost, func([]string) string { return "/dispatch-namespaces" }, func(c *cobra.Command, a []string) (any, error) {
		mode, _ := c.Flags().GetString("customer-mode")
		return nanoflare.CreateDispatchNamespaceInput{Name: a[0], CustomerMode: mode, Trust: "trusted"}, nil
	})
	create.Flags().String("customer-mode", "organization", "Customer identity mode: organization or platform")
	namespace.AddCommand(create)
	namespace.AddCommand(r.platformJSON("get <namespace-id>", "Inspect a namespace and grants", http.MethodGet, namespacePath, nil))
	namespace.AddCommand(r.platformJSON("rename <namespace-id> <name>", "Rename a namespace", http.MethodPatch, namespacePath, func(_ *cobra.Command, a []string) (any, error) {
		return nanoflare.RenameDispatchNamespaceInput{Name: a[1]}, nil
	}))
	namespace.AddCommand(r.platformJSON("delete <namespace-id>", "Delete an empty namespace", http.MethodDelete, namespacePath, nil))
	namespace.AddCommand(r.platformJSON("policies <namespace-id>", "Inspect immutable policy history", http.MethodGet, func(a []string) string { return namespacePath(a) + "/policies" }, nil))
	grants := &cobra.Command{Use: "grants", Short: "Inspect or replace explicit customer resource grants"}
	showHelpWhenEmpty(grants)
	grants.AddCommand(r.platformJSON("get <namespace-id>", "Inspect current grants and policy revision", http.MethodGet, namespacePath, nil))
	grants.AddCommand(r.platformJSON("set <namespace-id> <file>", "Replace grants using expected_revision and grants from JSON", http.MethodPut, func(a []string) string { return namespacePath(a) + "/grants" }, platformFile[nanoflare.NamespaceGrantsInput]))
	script := &cobra.Command{Use: "script", Short: "Manage private namespace scripts"}
	showHelpWhenEmpty(script)
	script.AddCommand(r.platformJSON("list <namespace-id>", "List private scripts", http.MethodGet, func(a []string) string { return namespacePath(a) + "/scripts" }, nil))
	createScript := r.platformJSON("create <namespace-id> <script-name>", "Create a private script with immutable identity", http.MethodPost, func(a []string) string { return namespacePath(a) + "/scripts" }, func(c *cobra.Command, a []string) (any, error) {
		customer, _ := c.Flags().GetString("customer-id")
		execution, _ := c.Flags().GetString("execution")
		return nanoflare.CreateNamespaceScriptInput{ScriptName: a[1], CustomerID: customer, Execution: execution}, nil
	})
	createScript.Flags().String("customer-id", "", "Customer ID required in platform customer mode")
	createScript.Flags().String("execution", "dynamic", "Execution mode: dynamic or static (immutable)")
	script.AddCommand(createScript)
	script.AddCommand(r.platformJSON("get <namespace-id> <script-name>", "Inspect a private script", http.MethodGet, scriptPath, nil))
	script.AddCommand(r.platformJSON("delete <namespace-id> <script-name>", "Delete a private script", http.MethodDelete, scriptPath, nil))
	root.AddCommand(namespace, grants, script)
	root.AddCommand(&cobra.Command{Use: "bundle <output-file>", Short: "Export this project as a complete deployment bundle with explicit resource IDs", Args: cobra.ExactArgs(1), RunE: func(_ *cobra.Command, args []string) error { return r.platformBundle(args[0]) }})
	root.AddCommand(r.platformJSON("inspect <worker-id>", "Inspect a Worker by immutable ID", http.MethodGet, platformWorkerPath, nil))
	for _, item := range []struct{ name, short, path string }{{"capabilities", "Inspect effective runtime capabilities", "/capabilities"}, {"publications", "Inspect recent publication stages", "/publications"}, {"deployments", "List retained revisions and weighted traffic", "/deployments"}} {
		suffix := item.path
		root.AddCommand(r.platformJSON(item.name+" <worker-id>", item.short, http.MethodGet, func(a []string) string { return platformWorkerPath(a) + suffix }, nil))
	}
	root.AddCommand(r.platformJSON("artifact <worker-id> <deployment-id>", "Inspect an immutable artifact manifest", http.MethodGet, func(a []string) string {
		return platformWorkerPath(a) + "/deployments/" + url.PathEscape(a[1]) + "/artifact"
	}, nil))
	root.AddCommand(r.platformJSON("deploy <worker-id> <file>", "Publish a full deployment from native DeployInput JSON", http.MethodPost, func(a []string) string { return platformWorkerPath(a) + "/deployments" }, platformFile[nanoflare.DeployInput]))
	root.AddCommand(r.platformJSON("traffic <worker-id> <file>", "Replace weighted revision traffic from JSON", http.MethodPut, func(a []string) string { return platformWorkerPath(a) + "/deployment-traffic" }, platformFile[platformTraffic]))
	root.AddCommand(r.platformJSON("rollback <worker-id> <deployment-id>", "Route all traffic to a retained revision", http.MethodPut, func(a []string) string { return platformWorkerPath(a) + "/deployment-traffic" }, func(_ *cobra.Command, a []string) (any, error) {
		return platformTraffic{Deployments: []nanoflare.DeploymentTraffic{{ID: a[1], TrafficPercent: 100}}}, nil
	}))
	return root
}

type platformTraffic struct {
	Deployments []nanoflare.DeploymentTraffic `json:"deployments"`
}

func namespacePath(a []string) string      { return "/dispatch-namespaces/" + url.PathEscape(a[0]) }
func scriptPath(a []string) string         { return namespacePath(a) + "/scripts/" + url.PathEscape(a[1]) }
func platformWorkerPath(a []string) string { return "/workers/" + url.PathEscape(a[0]) }
func platformFile[T any](_ *cobra.Command, a []string) (any, error) {
	data, err := os.ReadFile(a[len(a)-1])
	if err != nil {
		return nil, err
	}
	if len(data) > 10<<20 {
		return nil, errors.New("platform request file exceeds 10 MiB")
	}
	var result T
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(&result); err != nil {
		return nil, err
	}
	if err = decoder.Decode(new(any)); err != io.EOF {
		return nil, errors.New("platform request file must contain one JSON value")
	}
	return result, nil
}
func (r *Runner) platformJSON(use, short, method string, path func([]string) string, input func(*cobra.Command, []string) (any, error)) *cobra.Command {
	command := &cobra.Command{Use: use, Short: short, Args: argsFromUse(use)}
	command.RunE = func(c *cobra.Command, a []string) error {
		auth, _, err := resolveAuthConfig()
		if err != nil {
			return err
		}
		org := strings.TrimSpace(c.Flag("org").Value.String())
		if org == "" {
			org = auth.ActiveOrgID
		}
		if org == "" {
			return errors.New("select an organization with nanoflare auth use-org or --org")
		}
		var body any
		if input != nil {
			body, err = input(c, a)
			if err != nil {
				return err
			}
		}
		var result json.RawMessage
		target := strings.TrimRight(c.Flag("api-url").Value.String(), "/") + "/v1/organizations/" + url.PathEscape(org) + path(a)
		output := any(&result)
		if method == http.MethodDelete {
			output = nil
		}
		if err = r.request(method, target, body, output); err != nil {
			return err
		}
		if method == http.MethodDelete {
			fmt.Fprintln(r.Stdout, "Deleted")
			return nil
		}
		var formatted bytes.Buffer
		if err = json.Indent(&formatted, result, "", "  "); err != nil {
			return err
		}
		fmt.Fprintln(r.Stdout, formatted.String())
		return nil
	}
	return command
}
func (r *Runner) projectTarget(baseURL string, project Project) (nanoflare.App, error) {
	if strings.TrimSpace(project.WorkerID) == "" {
		return r.projectApp(baseURL, project.Name)
	}
	var detail nanoflare.WorkerDetail
	if err := r.request(http.MethodGet, baseURL+"/v1/workers/"+url.PathEscape(project.WorkerID), nil, &detail); err != nil {
		return nanoflare.App{}, err
	}
	if detail.App.ID != project.WorkerID {
		return nanoflare.App{}, errors.New("server returned a different Worker identity")
	}
	return detail.App, nil
}

func (r *Runner) platformBundle(output string) error {
	projectPath, project, err := loadDeploymentProject()
	if err != nil {
		return err
	}
	if err = validateCompatibilityFlags(project.CompatibilityFlags); err != nil {
		return err
	}
	for _, binding := range project.KVNamespaces {
		if strings.TrimSpace(binding.ID) == "" {
			return fmt.Errorf("bundle requires an explicit KV resource ID for %s", binding.Binding)
		}
	}
	for _, binding := range project.Databases {
		if strings.TrimSpace(binding.DatabaseID) == "" {
			return fmt.Errorf("bundle requires an explicit database ID for %s", binding.Binding)
		}
	}
	for _, binding := range project.ObjectStorageBuckets {
		if strings.TrimSpace(binding.BucketID) == "" {
			return fmt.Errorf("bundle requires an explicit bucket ID for %s", binding.Binding)
		}
	}
	for _, binding := range project.DispatchNamespaces {
		if strings.TrimSpace(binding.NamespaceID) == "" {
			return fmt.Errorf("bundle requires an explicit dispatch namespace ID for %s", binding.Binding)
		}
	}
	if _, err = generateWorkerTypes(project, "Env"); err != nil {
		return err
	}
	files, err := projectWorkerFiles(project)
	if err != nil {
		return err
	}
	assets, err := loadAssetFiles(project.Assets.Directory)
	if err != nil {
		return err
	}
	key, err := randomWebLoginState()
	if err != nil {
		return err
	}
	commitHash, commitMessage := deploymentGitMetadata(filepath.Dir(projectPath))
	input := nanoflare.DeployInput{IdempotencyKey: key, CommitHash: commitHash, CommitMessage: commitMessage, Files: files, Assets: assets, Entrypoint: project.Main, Format: project.Format, CompatibilityDate: project.CompatibilityDate, CompatibilityFlags: project.CompatibilityFlags, DNS: project.DNS, Triggers: project.Triggers, Vars: cloneProjectVars(project.Vars), KVNamespaces: project.KVNamespaces, Databases: project.Databases, ObjectStorageBuckets: project.ObjectStorageBuckets, DispatchNamespaces: project.DispatchNamespaces, Services: project.Services, AssetConfig: nanoflare.AssetConfig{Binding: project.Assets.Binding, HTMLHandling: project.Assets.HTMLHandling, NotFoundHandling: project.Assets.NotFoundHandling, RunWorkerFirst: project.Assets.RunWorkerFirst}}
	payload, err := json.MarshalIndent(input, "", "  ")
	if err != nil {
		return err
	}
	if len(payload) > 10<<20 {
		return errors.New("deployment bundle exceeds the 10 MiB control API limit")
	}
	file, err := os.OpenFile(output, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	_, err = file.Write(append(payload, '\n'))
	closeErr := file.Close()
	if err != nil {
		os.Remove(output)
		return err
	}
	if closeErr != nil {
		os.Remove(output)
		return closeErr
	}
	fmt.Fprintf(r.Stdout, "Exported deployment bundle to %s\n", output)
	return nil
}
