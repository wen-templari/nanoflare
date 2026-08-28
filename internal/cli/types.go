package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

const defaultTypesFilename = "worker-configuration.d.ts"

func (r *Runner) types(args []string) error {
	flags := flag.NewFlagSet("types", flag.ContinueOnError)
	flags.SetOutput(r.Stderr)
	envInterface := flags.String("env-interface", "Env", "Name of the generated environment interface")
	check := flags.Bool("check", false, "Check whether generated types are up to date")
	var configPaths stringSliceFlag
	flags.Var(&configPaths, "config", "Path to a Worker configuration file (repeatable)")
	flags.Var(&configPaths, "c", "Path to a Worker configuration file (repeatable)")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() > 1 {
		return errors.New("usage: nanoflare types [path] [-c config] [--env-interface Env] [--check]")
	}
	outputPath := defaultTypesFilename
	if flags.NArg() == 1 {
		outputPath = flags.Arg(0)
	}
	return r.typesWithOptions(outputPath, *envInterface, *check, configPaths)
}

type stringSliceFlag []string

func (values *stringSliceFlag) String() string { return strings.Join(*values, ",") }
func (values *stringSliceFlag) Set(value string) error {
	*values = append(*values, value)
	return nil
}

func (r *Runner) typesWithOptions(outputPath, envInterface string, check bool, configPaths []string) error {
	if !isTypeScriptIdentifier(envInterface) {
		return fmt.Errorf("--env-interface must be a TypeScript identifier, got %q", envInterface)
	}
	project, serviceTypes, err := loadTypeGenerationProject(configPaths, outputPath)
	if err != nil {
		return err
	}
	content, err := generateWorkerTypesWithServiceTypes(project, envInterface, serviceTypes)
	if err != nil {
		return err
	}
	if check {
		current, readErr := os.ReadFile(outputPath)
		if readErr == nil && bytes.Equal(current, content) {
			return nil
		}
		if readErr != nil && !errors.Is(readErr, os.ErrNotExist) {
			return fmt.Errorf("read %s: %w", outputPath, readErr)
		}
		return fmt.Errorf("generated types are out of date; run `nanoflare types %s`", outputPath)
	}
	if err := os.WriteFile(outputPath, content, 0o644); err != nil {
		return fmt.Errorf("write %s: %w", outputPath, err)
	}
	fmt.Fprintf(r.Stdout, "Generated TypeScript types at %s\n", outputPath)
	fmt.Fprintf(r.Stdout, "Add %s to the files or include list in your Worker tsconfig.json.\n", outputPath)
	return nil
}

// loadTypeGenerationProject resolves source-only configuration paths. The first
// config is the caller and every later config is available as a typed service
// target; none of these paths affect deploy-time configuration.
func loadTypeGenerationProject(configPaths []string, outputPath string) (Project, map[string]string, error) {
	configsSupplied := len(configPaths) > 0
	if len(configPaths) == 0 {
		configPaths = []string{projectFilename}
	}
	outputAbsolutePath, err := filepath.Abs(outputPath)
	if err != nil {
		return Project{}, nil, err
	}
	type targetProject struct {
		path    string
		project Project
	}
	targets := make(map[string]targetProject, len(configPaths)-1)
	var caller Project
	for index, configPath := range configPaths {
		absolutePath, err := filepath.Abs(configPath)
		if err != nil {
			return Project{}, nil, err
		}
		project, err := loadProjectAtPath(absolutePath, false)
		if err != nil {
			return Project{}, nil, err
		}
		if index == 0 {
			caller = project
			continue
		}
		if previous, exists := targets[project.Name]; exists {
			return Project{}, nil, fmt.Errorf("service target %q is configured by both %s and %s", project.Name, previous.path, absolutePath)
		}
		targets[project.Name] = targetProject{path: absolutePath, project: project}
	}
	serviceTypes := make(map[string]string, len(caller.Services))
	for _, service := range caller.Services {
		target, exists := targets[service.Service]
		if !exists {
			if !configsSupplied {
				continue // Preserve the existing untyped Fetcher output without -c.
			}
			return Project{}, nil, fmt.Errorf("service binding %q targets %q, but no supplied config has that name", service.Binding, service.Service)
		}
		entrypoint := filepath.Join(filepath.Dir(target.path), target.project.Main)
		relativeEntrypoint, err := filepath.Rel(filepath.Dir(outputAbsolutePath), entrypoint)
		if err != nil {
			return Project{}, nil, err
		}
		relativeEntrypoint = filepath.ToSlash(strings.TrimSuffix(relativeEntrypoint, filepath.Ext(relativeEntrypoint)))
		if !strings.HasPrefix(relativeEntrypoint, ".") {
			relativeEntrypoint = "./" + relativeEntrypoint
		}
		serviceTypes[service.Binding] = "Service<import(" + strconv.Quote(relativeEntrypoint) + ").default>"
	}
	return caller, serviceTypes, nil
}

type generatedBinding struct {
	name     string
	typeName string
}

func generateWorkerTypes(project Project, envInterface string) ([]byte, error) {
	return generateWorkerTypesWithServiceTypes(project, envInterface, nil)
}

func generateWorkerTypesWithServiceTypes(project Project, envInterface string, serviceTypes map[string]string) ([]byte, error) {
	bindings := make([]generatedBinding, 0, len(project.Vars)+len(project.Secrets.Required)+len(project.KVNamespaces)+len(project.Databases)+len(project.ObjectStorageBuckets)+len(project.Services)+1)
	seen := make(map[string]string)
	add := func(name, typeName, kind string) error {
		name = strings.TrimSpace(name)
		if name == "" {
			return fmt.Errorf("%s binding name is required", kind)
		}
		if previous, ok := seen[name]; ok {
			return fmt.Errorf("binding %q is defined by both %s and %s", name, previous, kind)
		}
		seen[name] = kind
		bindings = append(bindings, generatedBinding{name: name, typeName: typeName})
		return nil
	}

	varNames := make([]string, 0, len(project.Vars))
	for name := range project.Vars {
		varNames = append(varNames, name)
	}
	sort.Strings(varNames)
	for _, name := range varNames {
		typeName, err := jsonLiteralType(project.Vars[name])
		if err != nil {
			return nil, fmt.Errorf("vars.%s must be valid JSON: %w", name, err)
		}
		if err := add(name, typeName, "vars"); err != nil {
			return nil, err
		}
	}
	requiredSecrets, err := requiredSecretNames(project.Secrets.Required)
	if err != nil {
		return nil, err
	}
	for _, name := range requiredSecrets {
		if err := add(name, "string", "secrets.required"); err != nil {
			return nil, err
		}
	}
	for _, binding := range project.KVNamespaces {
		if err := add(binding.Binding, "NanoflareKVNamespace", "kv_namespaces"); err != nil {
			return nil, err
		}
	}
	for _, binding := range project.Databases {
		if err := add(binding.Binding, "NanoflareD1Database", "db"); err != nil {
			return nil, err
		}
	}
	assetBinding := strings.TrimSpace(project.Assets.Binding)
	if assetBinding == "" {
		assetBinding = "ASSETS"
	}
	if err := add(assetBinding, "NanoflareAssetFetcher", "assets"); err != nil {
		return nil, err
	}
	for _, binding := range project.ObjectStorageBuckets {
		if err := add(binding.Binding, "NanoflareObjectStorageBucket", "object_storage_buckets"); err != nil {
			return nil, err
		}
	}
	for _, binding := range project.Services {
		typeName := "Fetcher"
		if resolvedType, ok := serviceTypes[binding.Binding]; ok {
			typeName = resolvedType
		}
		if err := add(binding.Binding, typeName, "services"); err != nil {
			return nil, err
		}
	}
	sort.Slice(bindings, func(i, j int) bool { return bindings[i].name < bindings[j].name })

	var output strings.Builder
	output.WriteString("/* eslint-disable */\n")
	output.WriteString("// Generated by Nanoflare by running `nanoflare types`. DO NOT EDIT.\n\n")
	output.WriteString(nanoflareRuntimeTypeDeclarations)
	output.WriteString("\n\n")
	fmt.Fprintf(&output, "interface %s {\n", envInterface)
	for _, binding := range bindings {
		fmt.Fprintf(&output, "  %s: %s;\n", typeScriptPropertyName(binding.name), binding.typeName)
	}
	output.WriteString("}\n")
	return []byte(output.String()), nil
}

func requiredSecretNames(names []string) ([]string, error) {
	if len(names) == 0 {
		return nil, nil
	}
	required := make([]string, 0, len(names))
	seen := make(map[string]struct{}, len(names))
	for index, name := range names {
		name = strings.TrimSpace(name)
		if name == "" {
			return nil, fmt.Errorf("secrets.required[%d] must be a non-empty secret name", index)
		}
		if _, exists := seen[name]; exists {
			return nil, fmt.Errorf("secrets.required contains duplicate secret name %q", name)
		}
		seen[name] = struct{}{}
		required = append(required, name)
	}
	sort.Strings(required)
	return required, nil
}

func jsonLiteralType(raw json.RawMessage) (string, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return "", err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return "", errors.New("contains multiple values")
	}
	return typeScriptJSONValue(value), nil
}

func typeScriptJSONValue(value any) string {
	switch value := value.(type) {
	case nil:
		return "null"
	case bool:
		return strconv.FormatBool(value)
	case string:
		return strconv.Quote(value)
	case json.Number:
		return value.String()
	case []any:
		values := make([]string, len(value))
		for index, item := range value {
			values[index] = typeScriptJSONValue(item)
		}
		return "[" + strings.Join(values, ", ") + "]"
	case map[string]any:
		keys := make([]string, 0, len(value))
		for key := range value {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		properties := make([]string, 0, len(keys))
		for _, key := range keys {
			properties = append(properties, typeScriptPropertyName(key)+": "+typeScriptJSONValue(value[key]))
		}
		return "{ " + strings.Join(properties, "; ") + " }"
	default:
		return "unknown"
	}
}

func typeScriptPropertyName(name string) string {
	if isTypeScriptIdentifier(name) {
		return name
	}
	return strconv.Quote(name)
}

func isTypeScriptIdentifier(name string) bool {
	if name == "" {
		return false
	}
	for index, char := range name {
		if (char < 'a' || char > 'z') && (char < 'A' || char > 'Z') && char != '_' && char != '$' && (index == 0 || char < '0' || char > '9') {
			return false
		}
	}
	return true
}

const nanoflareRuntimeTypeDeclarations = `interface NanoflareObjectHTTPMetadata {
  contentType?: string;
}

interface NanoflareScheduledController {
  scheduledTime: number;
  cron: string;
  noRetry(): void;
}

interface NanoflareExecutionContext {
  waitUntil(promise: Promise<unknown>): void;
}

interface NanoflareWorkerHandler<Env = unknown> {
  fetch?: (
    request: Request,
    env: Env,
    ctx: NanoflareExecutionContext,
  ) => Response | Promise<Response>;
  scheduled?: (
    controller: NanoflareScheduledController,
    env: Env,
    ctx: NanoflareExecutionContext,
  ) => void | Promise<void>;
}

interface NanoflareObjectStorageObject {
  key: string;
  size: number;
  etag: string;
  httpEtag: string;
  uploaded: Date;
  httpMetadata: NanoflareObjectHTTPMetadata;
}

interface NanoflareObjectStorageObjectBody extends NanoflareObjectStorageObject {
  body: ReadableStream | null;
  readonly bodyUsed: boolean;
  arrayBuffer(): Promise<ArrayBuffer>;
  text(): Promise<string>;
  json<T = unknown>(): Promise<T>;
  blob(): Promise<Blob>;
}

interface NanoflareObjectStoragePutOptions {
  httpMetadata?: NanoflareObjectHTTPMetadata;
}

type NanoflareObjectStoragePutValue = string | ArrayBuffer | ArrayBufferView | Blob | ReadableStream | Request | Response;

interface NanoflareObjectStorageBucket {
  put(key: string, value: NanoflareObjectStoragePutValue, options?: NanoflareObjectStoragePutOptions): Promise<NanoflareObjectStorageObject>;
  get(key: string): Promise<NanoflareObjectStorageObjectBody | null>;
  head(key: string): Promise<NanoflareObjectStorageObject | null>;
  delete(key: string): Promise<void>;
}

interface NanoflareKVNamespaceGetOptions<Type> {
  type?: Type;
}

interface NanoflareKVNamespacePutOptions {
  expiration?: number;
  expirationTtl?: number;
}

interface NanoflareKVNamespaceListOptions {
  limit?: number;
  prefix?: string | null;
  cursor?: string | null;
}

interface NanoflareKVNamespaceListKey<Metadata = unknown> {
  name: string;
  expiration?: number;
  metadata?: Metadata;
}

interface NanoflareKVNamespaceListResult<Metadata = unknown> {
  keys: NanoflareKVNamespaceListKey<Metadata>[];
  list_complete: boolean;
  cursor?: string;
}

interface NanoflareKVNamespace {
  get(key: string, options?: Partial<NanoflareKVNamespaceGetOptions<undefined>>): Promise<string | null>;
  get(key: string, type: "text"): Promise<string | null>;
  get<T = unknown>(key: string, type: "json"): Promise<T | null>;
  get(key: string, type: "arrayBuffer"): Promise<ArrayBuffer | null>;
  get(key: string, type: "stream"): Promise<ReadableStream | null>;
  get(key: string, options: NanoflareKVNamespaceGetOptions<"text">): Promise<string | null>;
  get<T = unknown>(key: string, options: NanoflareKVNamespaceGetOptions<"json">): Promise<T | null>;
  get(key: string, options: NanoflareKVNamespaceGetOptions<"arrayBuffer">): Promise<ArrayBuffer | null>;
  get(key: string, options: NanoflareKVNamespaceGetOptions<"stream">): Promise<ReadableStream | null>;
  put(key: string, value: string | ArrayBuffer | ArrayBufferView | ReadableStream, options?: NanoflareKVNamespacePutOptions): Promise<void>;
  delete(key: string): Promise<void>;
  list<Metadata = unknown>(options?: NanoflareKVNamespaceListOptions): Promise<NanoflareKVNamespaceListResult<Metadata>>;
}

type NanoflareD1BindValue = string | number | boolean | null | ArrayBuffer | ArrayBufferView;

interface NanoflareD1Meta {
  served_by: string;
  served_by_primary: boolean;
  duration: number;
  changes: number;
  last_row_id: number;
  changed_db: boolean;
  size_after: number;
  rows_read: number;
  rows_written: number;
}

interface NanoflareD1Result<T = Record<string, unknown>> {
  success: boolean;
  meta: NanoflareD1Meta;
  results: T[];
}

interface NanoflareD1ExecResult {
  count: number;
  duration: number;
}

interface NanoflareD1PreparedStatement {
  bind(...values: NanoflareD1BindValue[]): NanoflareD1PreparedStatement;
  run<T = Record<string, unknown>>(): Promise<NanoflareD1Result<T>>;
  all<T = Record<string, unknown>>(): Promise<NanoflareD1Result<T>>;
  raw<T = unknown[]>(options?: { columnNames?: boolean }): Promise<T[]>;
  first<T = Record<string, unknown>>(columnName?: string): Promise<T | null>;
}

interface NanoflareD1DatabaseSession {
  getBookmark(): string | null;
  prepare(query: string): NanoflareD1PreparedStatement;
  batch<T = Record<string, unknown>>(statements: NanoflareD1PreparedStatement[]): Promise<NanoflareD1Result<T>[]>;
}

interface NanoflareD1Database {
  prepare(query: string): NanoflareD1PreparedStatement;
  batch<T = Record<string, unknown>>(statements: NanoflareD1PreparedStatement[]): Promise<NanoflareD1Result<T>[]>;
  exec(query: string): Promise<NanoflareD1ExecResult>;
  withSession(initialBookmark?: string): NanoflareD1DatabaseSession;
}

interface NanoflareAssetFetcher {
  fetch(input: RequestInfo | URL, init?: RequestInit): Promise<Response>;
}

interface Fetcher {
  fetch(input: RequestInfo | URL, init?: RequestInit): Promise<Response>;
}

type Service<T> = Fetcher & {
  [Method in keyof T as Method extends "fetch"
    ? never
    : T[Method] extends (...args: any[]) => any
      ? Method
      : never]: T[Method] extends (...args: infer Arguments) => infer Result
    ? (...args: Arguments) => Promise<Awaited<Result>>
    : never;
};`
