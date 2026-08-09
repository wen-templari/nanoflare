package cli

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// newRootCommand describes the public CLI surface. The Runner methods remain
// responsible for the API and filesystem work, while Cobra owns routing,
// help, and flag parsing.
func (r *Runner) newRootCommand() *cobra.Command {
	root := &cobra.Command{
		Use:                "nanoflare",
		Short:              "Develop, deploy, and manage Nanoflare resources",
		SilenceErrors:      true,
		SilenceUsage:       false,
		DisableSuggestions: true,
		CompletionOptions:  cobra.CompletionOptions{DisableDefaultCmd: true},
	}
	showHelpWhenEmpty(root)
	root.SetOut(r.Stdout)
	root.SetErr(r.Stderr)

	root.AddCommand(
		r.initCommand(),
		r.leaf("check", "Validate the current worker project before deployment", r.check, func(c *cobra.Command) {
			c.Flags().Bool("types", false, "Also verify worker-configuration.d.ts is current")
		}),
		r.typesCommand(),
		r.workerLeaf("create", "Create the current worker", r.create, apiURLFlag("")),
		r.workerLeaf("list", "List workers", r.list, apiURLFlag(envOrDefault("NANOFLARED_URL", defaultAPIURL))),
		r.workerLeaf("delete [worker-id]", "Delete a worker", r.delete, apiURLFlag("")),
		r.workerLeaf("deploy", "Deploy the current worker", r.deploy, func(c *cobra.Command) {
			apiURLFlag("")(c)
			c.Flags().String("compatibility-date", "", "Worker compatibility date (YYYY-MM-DD)")
			c.Flags().Bool("provision", true, "Create missing KV namespaces, databases, and object-storage buckets")
		}),
		r.deploymentCommand(), r.authCommand(), r.secretCommand(), r.kvCommand(), r.dbCommand(), r.objectStorageCommand(),
	)
	return root
}

func (r *Runner) typesCommand() *cobra.Command {
	var configPaths []string
	var envInterface string
	var check bool
	command := &cobra.Command{
		Use:   "types [path]",
		Short: "Generate TypeScript types for the current worker",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			outputPath := defaultTypesFilename
			if len(args) == 1 {
				outputPath = args[0]
			}
			return r.typesWithOptions(outputPath, envInterface, check, configPaths)
		},
	}
	command.Flags().StringArrayVarP(&configPaths, "config", "c", nil, "Path to a Worker configuration file (repeatable)")
	command.Flags().StringVar(&envInterface, "env-interface", "Env", "Name of the generated environment interface")
	command.Flags().BoolVar(&check, "check", false, "Check whether generated types are up to date")
	return command
}

func (r *Runner) initCommand() *cobra.Command {
	return &cobra.Command{
		Use:                "init [create-nanoflare options]",
		Short:              "Create a worker project",
		Long:               "Create a worker project with npm create nanoflare@latest.",
		DisableFlagParsing: true,
		RunE: func(_ *cobra.Command, args []string) error {
			return r.init(args)
		},
	}
}

type commandFlags func(*cobra.Command)

func apiURLFlag(defaultValue string) commandFlags {
	return func(c *cobra.Command) { c.Flags().String("api-url", defaultValue, "Nanoflare API URL") }
}

func showHelpWhenEmpty(c *cobra.Command) {
	c.Args = cobra.NoArgs
	c.RunE = func(cmd *cobra.Command, _ []string) error {
		return cmd.Help()
	}
}

func (r *Runner) leaf(use, short string, run func([]string) error, flags commandFlags) *cobra.Command {
	c := &cobra.Command{Use: use, Short: short, Args: argsFromUse(use)}
	c.RunE = r.runLegacy(c, run)
	c.SetFlagErrorFunc(legacyFlagError)
	if flags != nil {
		flags(c)
	}
	return c
}

func legacyFlagError(_ *cobra.Command, err error) error {
	const unknownFlag = "unknown flag: --"
	if name, ok := strings.CutPrefix(err.Error(), unknownFlag); ok {
		return fmt.Errorf("flag provided but not defined: -%s", name)
	}
	return err
}

func (r *Runner) workerLeaf(use, short string, run func([]string) error, flags commandFlags) *cobra.Command {
	c := r.leaf(use, short, func(args []string) error { return run(withoutWorkerNoun(args)) }, flags)
	baseArgs := c.Args
	c.Args = func(cmd *cobra.Command, args []string) error {
		return baseArgs(cmd, withoutWorkerNoun(args))
	}
	return c
}

func argsFromUse(use string) cobra.PositionalArgs {
	parts := strings.Fields(use)
	required, optional := 0, 0
	for _, part := range parts[1:] {
		if strings.HasPrefix(part, "<") && strings.HasSuffix(part, ">") {
			required++
		}
		if strings.HasPrefix(part, "[") && strings.HasSuffix(part, "]") {
			optional++
		}
	}
	return cobra.RangeArgs(required, required+optional)
}

func (r *Runner) runLegacy(command *cobra.Command, run func([]string) error) func(*cobra.Command, []string) error {
	return func(_ *cobra.Command, args []string) error {
		legacyArgs := make([]string, 0, len(args)+command.Flags().NFlag())
		command.Flags().Visit(func(flag *pflag.Flag) {
			if flag.Value.Type() == "bool" && flag.Value.String() == "true" {
				legacyArgs = append(legacyArgs, "--"+flag.Name)
				return
			}
			legacyArgs = append(legacyArgs, "--"+flag.Name+"="+flag.Value.String())
		})
		legacyArgs = append(legacyArgs, args...)
		return run(legacyArgs)
	}
}

func (r *Runner) deploymentCommand() *cobra.Command {
	c := &cobra.Command{Use: "deployment", Short: "Inspect worker deployments and logs"}
	showHelpWhenEmpty(c)
	c.AddCommand(r.leaf("output [worker-id]", "Show captured worker output", r.deploymentOutput, func(c *cobra.Command) {
		apiURLFlag("")(c)
		c.Flags().String("deployment", "", "Deployment ID")
		c.Flags().String("level", "", "Output level")
		c.Flags().String("search", "", "Text to search for")
		c.Flags().Int("limit", 500, "Maximum output lines (1-1000)")
		c.Flags().String("since", "", "RFC3339 start time")
		c.Flags().String("until", "", "RFC3339 end time")
	}))
	return c
}

func (r *Runner) authCommand() *cobra.Command {
	c := &cobra.Command{Use: "auth", Short: "Sign in and manage organizations"}
	showHelpWhenEmpty(c)
	c.AddCommand(
		r.leaf("login [token]", "Sign in to Nanoflare", r.authLogin, func(c *cobra.Command) {
			apiURLFlag(envOrDefault("NANOFLARED_URL", defaultAPIURL))(c)
			c.Flags().Bool("web", false, "Use the browser sign-in flow")
			c.Flags().Bool("pat", false, "Use the personal access token sign-in flow")
			c.Flags().String("pat-token", "", "Personal access token for non-interactive sign-in")
		}),
		r.leaf("orgs", "List organizations", r.authOrgs, nil),
		r.leaf("use-org <org-id>", "Select the active organization", r.authUseOrg, nil),
		r.leaf("whoami", "Show the current user", r.authWhoami, nil),
		r.leaf("logout", "Log out", r.authLogout, nil),
	)
	return c
}

func (r *Runner) secretCommand() *cobra.Command {
	c := &cobra.Command{Use: "secret", Short: "Manage secrets for the current worker"}
	showHelpWhenEmpty(c)
	c.AddCommand(r.leaf("put <name> <value>", "Create or update a secret", r.secretPut, apiURLFlag("")), r.leaf("list", "List secrets", r.secretList, apiURLFlag("")), r.leaf("delete <name>", "Delete a secret", r.secretDelete, apiURLFlag("")))
	return c
}

func (r *Runner) kvCommand() *cobra.Command {
	kv := &cobra.Command{Use: "kv", Short: "Manage KV storage namespaces"}
	showHelpWhenEmpty(kv)
	namespace := &cobra.Command{Use: "namespace", Short: "Create, list, and delete KV namespaces"}
	showHelpWhenEmpty(namespace)
	namespace.AddCommand(r.leaf("create <name>", "Create a KV namespace", r.kvNamespaceCreate, apiURLFlag(envOrDefault("NANOFLARED_URL", defaultAPIURL))), r.leaf("list", "List KV namespaces", r.kvNamespaceList, apiURLFlag(envOrDefault("NANOFLARED_URL", defaultAPIURL))), r.leaf("delete <namespace-id>", "Delete a KV namespace", r.kvNamespaceDelete, apiURLFlag(envOrDefault("NANOFLARED_URL", defaultAPIURL))))
	kv.AddCommand(namespace)
	return kv
}

func (r *Runner) dbCommand() *cobra.Command {
	db := &cobra.Command{Use: "db", Short: "Manage SQLite databases"}
	showHelpWhenEmpty(db)
	api := apiURLFlag(envOrDefault("NANOFLARED_URL", defaultAPIURL))
	execute := r.leaf("execute [database-id]", "Run one SQL statement", r.dbExecute, func(c *cobra.Command) {
		api(c)
		c.Flags().String("binding", "", "Database binding in nanoflare.json")
		c.Flags().String("command", "", "SQL statement to run")
		c.Flags().String("file", "", "Path to a file containing one SQL statement")
		c.Flags().Bool("json", false, "Print the complete response as JSON")
	})
	execute.Long = "Run one SQL statement against a database. Use --command for inline SQL or --file for a file containing one statement. Use migrations for multi-statement schema changes."
	execute.Example = "  nanoflare db execute db_123 --command \"CREATE TABLE messages (body text)\"\n  nanoflare db execute db_123 --command \"SELECT body FROM messages\"\n  nanoflare db execute db_123 --file query.sql"
	db.AddCommand(r.leaf("create <name>", "Create a database", r.dbCreate, api), r.leaf("list", "List databases", r.dbList, api), r.leaf("delete <database-id>", "Delete a database", r.dbDelete, api), execute)
	migrations := &cobra.Command{Use: "migrations", Short: "Create and apply database migrations"}
	showHelpWhenEmpty(migrations)
	migrations.AddCommand(r.leaf("create <name>", "Create a timestamped migration file", r.dbMigrationsCreate, func(c *cobra.Command) { c.Flags().String("path", "migrations", "Migrations directory") }), r.leaf("apply [database-id]", "Apply migration files to a database", r.dbMigrationsApply, func(c *cobra.Command) {
		api(c)
		c.Flags().String("path", "migrations", "Migrations directory")
		c.Flags().String("binding", "", "Database binding in nanoflare.json")
	}))
	db.AddCommand(migrations)
	return db
}

func (r *Runner) objectStorageCommand() *cobra.Command {
	storage := &cobra.Command{Use: "object-storage", Short: "Manage object storage buckets"}
	showHelpWhenEmpty(storage)
	bucket := &cobra.Command{Use: "bucket", Short: "Create, list, and delete storage buckets"}
	showHelpWhenEmpty(bucket)
	api := apiURLFlag(envOrDefault("NANOFLARED_URL", defaultAPIURL))
	bucket.AddCommand(r.leaf("create <name>", "Create a bucket", r.objectStorageBucketCreate, api), r.leaf("list", "List buckets", r.objectStorageBucketList, api), r.leaf("delete <bucket-id>", "Delete a bucket", r.objectStorageBucketDelete, api))
	storage.AddCommand(bucket)
	return storage
}
