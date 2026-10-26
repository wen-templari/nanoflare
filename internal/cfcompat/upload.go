// Package cfcompat translates the supported Cloudflare management wire format
// into complete native revision snapshots. It performs no persistence or network
// requests: callers must authorize the account, resolve scoped resources and
// atomically publish the returned candidate through the native service.
package cfcompat

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"path"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/clas/nanoflare/internal/nanoflare"
)

const MaxUploadBytes = 10 << 20

// Binding is the management representation, never a runtime authority handle.
// Unknown and type-inappropriate fields are rejected by DecodeMetadata.
type Binding struct {
	Name        string          `json:"name"`
	Type        string          `json:"type"`
	Text        *string         `json:"text,omitempty"`
	JSON        json.RawMessage `json:"json,omitempty"`
	NamespaceID string          `json:"namespace_id,omitempty"`
	Namespace   string          `json:"namespace,omitempty"`
	ID          string          `json:"id,omitempty"` // Deprecated D1 identifier.
	DatabaseID  string          `json:"database_id,omitempty"`
	BucketName  string          `json:"bucket_name,omitempty"`
	Service     string          `json:"service,omitempty"`
	Environment string          `json:"environment,omitempty"`
	OldName     string          `json:"old_name,omitempty"`
	VersionID   string          `json:"version_id,omitempty"`
}
type Metadata struct {
	Assets             *AssetUploadMetadata `json:"assets,omitempty"`
	MainModule         string               `json:"main_module"`
	CompatibilityDate  string               `json:"compatibility_date,omitempty"`
	CompatibilityFlags []string             `json:"compatibility_flags,omitempty"`
	Bindings           []Binding            `json:"bindings,omitempty"`
	KeepBindings       []string             `json:"keep_bindings,omitempty"`
	KeepAssets         bool                 `json:"keep_assets,omitempty"`
	Tags               *[]string            `json:"tags,omitempty"`
	StrictInherit      bool                 `json:"-"`
}
type AssetUploadMetadata struct {
	JWT    string                 `json:"jwt"`
	Config *nanoflare.AssetConfig `json:"config,omitempty"`
}
type Previous struct {
	Deployment nanoflare.DeployInput
	Bindings   []Binding
	Tags       []string
}
type Candidate struct {
	Deployment  nanoflare.DeployInput
	Bindings    []Binding
	Tags        []string
	Secrets     map[string]string `json:"-"`
	KeepSecrets map[string]string
}

// Resolver must resolve names within the already authenticated organization.
// Resource grants are checked again by the native deployment service.
type Resolver func(kind, name string) (string, error)

type Options struct {
	// Only the ordinary Worker upload route may grant namespace-wide dispatch.
	AllowDispatchNamespaces bool
	LoadCompletedAssets     func() ([]nanoflare.AssetFile, error)
	StrictInherit           bool
	LoadRetainedAssets      func() ([]nanoflare.AssetFile, error)
}

// DecodeObject shares the upload decoder's duplicate/unknown-field checks with
// management JSON endpoints. Wire keys are exact, including case.
func DecodeObject(data []byte, target any, fields ...string) error {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 || trimmed[0] != '{' || !utf8.Valid(data) {
		return errors.New("a UTF-8 JSON object is required")
	}
	if err := strictJSON(data, target); err != nil {
		return err
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(data, &object); err != nil {
		return err
	}
	return exactFields(object, fields...)
}

func strictJSON(data []byte, target any) error {
	if err := uniqueJSONKeys(data); err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return errors.New("invalid or unsupported metadata field")
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return errors.New("metadata must contain exactly one JSON value")
	}
	return nil
}

// uniqueJSONKeys prevents conflicting metadata from being interpreted differently
// by validation, snapshot construction and future request-signing clients.
func uniqueJSONKeys(data []byte) error {
	d := json.NewDecoder(bytes.NewReader(data))
	var value func(int) error
	value = func(depth int) error {
		if depth > 64 {
			return errors.New("metadata nesting limit exceeded")
		}
		token, err := d.Token()
		if err != nil {
			return errors.New("invalid metadata JSON")
		}
		delim, ok := token.(json.Delim)
		if !ok {
			return nil
		}
		switch delim {
		case '{':
			seen := map[string]bool{}
			for d.More() {
				key, err := d.Token()
				if err != nil {
					return errors.New("invalid metadata JSON")
				}
				name, ok := key.(string)
				if !ok || seen[name] {
					return errors.New("duplicate metadata JSON key")
				}
				seen[name] = true
				if err := value(depth + 1); err != nil {
					return err
				}
			}
		case '[':
			for d.More() {
				if err := value(depth + 1); err != nil {
					return err
				}
			}
		default:
			return errors.New("invalid metadata JSON")
		}
		_, err = d.Token()
		return err
	}
	if err := value(0); err != nil {
		return err
	}
	if _, err := d.Token(); err != io.EOF {
		return errors.New("metadata must contain exactly one JSON value")
	}
	return nil
}

func DecodeMetadata(data []byte) (Metadata, error) {
	var metadata Metadata
	if !utf8.Valid(data) {
		return metadata, errors.New("metadata must be UTF-8")
	}
	if len(data) > 1<<20 {
		return metadata, errors.New("metadata exceeds 1 MiB")
	}
	if len(bytes.TrimSpace(data)) == 0 || bytes.TrimSpace(data)[0] != '{' {
		return metadata, errors.New("metadata must be an object")
	}
	if err := strictJSON(data, &metadata); err != nil {
		return metadata, err
	}
	// encoding/json also matches struct fields without regard to case. The wire
	// contract uses exact keys, so reject aliases before they can override a key.
	var fields map[string]json.RawMessage
	_ = json.Unmarshal(data, &fields)
	if err := exactFields(fields, "main_module", "compatibility_date", "compatibility_flags", "bindings", "keep_bindings", "keep_assets", "tags", "assets"); err != nil {
		return metadata, err
	}
	if raw, exists := fields["assets"]; exists {
		var object map[string]json.RawMessage
		if json.Unmarshal(raw, &object) != nil || object == nil || metadata.Assets == nil || metadata.Assets.JWT == "" || len(metadata.Assets.JWT) > 2048 || metadata.KeepAssets {
			return metadata, errors.New("assets requires a completion token and cannot combine with keep_assets")
		}
		if err := exactFields(object, "jwt", "config"); err != nil {
			return metadata, err
		}
		if config, exists := object["config"]; exists {
			var configFields map[string]json.RawMessage
			if json.Unmarshal(config, &configFields) != nil || configFields == nil {
				return metadata, errors.New("assets config must be an object")
			}
			if err := exactFields(configFields, "html_handling", "not_found_handling", "run_worker_first"); err != nil {
				return metadata, err
			}
		}
	}
	var bindings []map[string]json.RawMessage
	_ = json.Unmarshal(fields["bindings"], &bindings)
	for _, binding := range bindings {
		if err := exactFields(binding, "name", "type", "text", "json", "namespace_id", "namespace", "id", "database_id", "bucket_name", "service", "environment", "old_name", "version_id"); err != nil {
			return metadata, err
		}
	}
	if !moduleName(metadata.MainModule) {
		return metadata, errors.New("main_module must identify a relative JavaScript module")
	}
	if metadata.CompatibilityDate == "" {
		metadata.CompatibilityDate = "2021-11-02"
	}
	if len(metadata.Bindings) > 128 || len(metadata.KeepBindings) > 16 {
		return metadata, errors.New("binding limit exceeded")
	}
	if metadata.Tags != nil {
		if err := ValidateTags(*metadata.Tags); err != nil {
			return metadata, err
		}
	}
	return metadata, nil
}

func exactFields(fields map[string]json.RawMessage, names ...string) error {
	allowed := map[string]bool{}
	for _, name := range names {
		allowed[name] = true
	}
	for name := range fields {
		if !allowed[name] {
			return errors.New("invalid or unsupported metadata field")
		}
	}
	return nil
}

func moduleName(name string) bool {
	for _, r := range name {
		if r < 32 || r == 127 {
			return false
		}
	}
	return name != "" && name != "." && len(name) <= 512 && path.Clean(name) == name && !strings.HasPrefix(name, "/") && name != ".." && !strings.HasPrefix(name, "../") && !strings.ContainsAny(name, "\\")
}

// DecodedUpload keeps the bounded wire request separate from resolved state so
// an HTTP retry can be identified before resolving inheritance again.
type DecodedUpload struct {
	Metadata Metadata               `json:"-"`
	Files    []nanoflare.WorkerFile `json:"-"`
}

func (upload DecodedUpload) IdentityPayload() ([]byte, error) {
	return json.Marshal(struct {
		Metadata Metadata
		Files    []nanoflare.WorkerFile
	}{upload.Metadata, upload.Files})
}
func DecodeUpload(contentType string, body io.Reader) (DecodedUpload, error) {
	return decodeUpload(contentType, body, DecodeMetadata)
}

// DecodeContentUpload only accepts module selection. Content replacement cannot
// mutate settings or bindings through the full-upload metadata schema.
func DecodeContentUpload(contentType string, body io.Reader) (DecodedUpload, error) {
	return decodeUpload(contentType, body, func(data []byte) (Metadata, error) {
		var metadata struct {
			MainModule string `json:"main_module"`
		}
		if err := DecodeObject(data, &metadata, "main_module"); err != nil {
			return Metadata{}, err
		}
		return DecodeMetadata(data)
	})
}

func decodeUpload(contentType string, body io.Reader, decodeMetadata func([]byte) (Metadata, error)) (DecodedUpload, error) {
	data, err := io.ReadAll(io.LimitReader(body, MaxUploadBytes+1))
	if err != nil {
		return DecodedUpload{}, err
	}
	if len(data) > MaxUploadBytes {
		return DecodedUpload{}, errors.New("upload exceeds 10 MiB")
	}
	typeName, params, err := mime.ParseMediaType(contentType)
	// SDK 7 retains application/javascript even though fetch encodes FormData.
	// Accept only an initial, bounded MIME delimiter, with no preamble or type
	// parameters. All parts still pass the normal strict upload validation.
	if err == nil && typeName == "application/javascript" && len(params) == 0 {
		end := bytes.Index(data, []byte("\r\n"))
		if end > 2 && end <= 72 && bytes.HasPrefix(data, []byte("--")) {
			boundary := string(data[2:end])
			probe := multipart.NewWriter(io.Discard)
			if probe.SetBoundary(boundary) == nil {
				typeName, params = "multipart/form-data", map[string]string{"boundary": boundary}
			}
		}
	}
	if err != nil || typeName != "multipart/form-data" || params["boundary"] == "" {
		return DecodedUpload{}, errors.New("multipart/form-data with a boundary is required")
	}
	reader := multipart.NewReader(bytes.NewReader(data), params["boundary"])
	parts := map[string][]byte{}
	wasmParts := map[string]bool{}
	sdkMetadata := map[string]any{}
	for count := 0; ; count++ {
		part, err := reader.NextRawPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			return DecodedUpload{}, errors.New("invalid multipart upload")
		}
		if count >= 256 {
			return DecodedUpload{}, errors.New("too many upload parts")
		}
		name := part.FormName()
		if strings.HasPrefix(name, "metadata[") {
			if len(part.Header.Values("Content-Disposition")) != 1 || len(part.Header.Values("Content-Type")) != 0 || part.FileName() != "" || part.Header.Get("Content-Transfer-Encoding") != "" || part.Header.Get("Content-Encoding") != "" {
				return DecodedUpload{}, errors.New("invalid SDK metadata field")
			}
			value, err := io.ReadAll(part)
			if err != nil || !utf8.Valid(value) || len(value) > 1<<20 {
				return DecodedUpload{}, errors.New("invalid SDK metadata value")
			}
			if err := addSDKMetadata(sdkMetadata, name, string(value)); err != nil {
				return DecodedUpload{}, err
			}
			continue
		}
		// Cloudflare SDKs use repeated `files` or `files[]` parts with the module identity
		// in filename. Other clients use the module name as the form key.
		// Never let a filename override a named module (or metadata).
		if name == "files" || name == "files[]" {
			_, disposition, parseErr := mime.ParseMediaType(part.Header.Get("Content-Disposition"))
			if parseErr != nil || !moduleName(disposition["filename"]) {
				return DecodedUpload{}, errors.New("files parts require a valid module filename")
			}
			name = disposition["filename"]
			if name == "metadata" {
				return DecodedUpload{}, errors.New("module filename cannot replace metadata")
			}
		}
		if len(part.Header.Values("Content-Type")) > 1 || (name != "metadata" && len(part.Header.Values("Content-Type")) != 1) || len(part.Header.Values("Content-Disposition")) != 1 || part.Header.Get("Content-Transfer-Encoding") != "" || part.Header.Get("Content-Encoding") != "" {
			return DecodedUpload{}, errors.New("ambiguous or encoded upload parts are unsupported")
		}
		if !moduleName(name) {
			return DecodedUpload{}, errors.New("invalid upload part name")
		}
		if _, exists := parts[name]; exists {
			return DecodedUpload{}, errors.New("duplicate upload part")
		}
		contentType := part.Header.Get("Content-Type")
		// Wrangler emits metadata as a plain form field. Its contents still
		// pass the exact JSON-object decoder; source modules require MIME types.
		if name == "metadata" && contentType == "" {
			contentType = "application/json"
		}
		partType, _, err := mime.ParseMediaType(contentType)
		if err != nil {
			return DecodedUpload{}, errors.New("upload parts require a content type")
		}
		if name == "metadata" {
			if partType != "application/json" {
				return DecodedUpload{}, errors.New("metadata content type must be application/json")
			}
		} else if partType != "application/wasm" && partType != "application/javascript+module" && partType != "text/javascript+module" && partType != "application/javascript" && partType != "text/javascript" {
			return DecodedUpload{}, errors.New("unsupported module content type")
		}
		value, err := io.ReadAll(part)
		if err != nil {
			return DecodedUpload{}, errors.New("invalid upload part")
		}
		if partType == "application/wasm" {
			if len(value) < 8 || !bytes.Equal(value[:8], []byte{0, 97, 115, 109, 1, 0, 0, 0}) {
				return DecodedUpload{}, errors.New("invalid Wasm module header")
			}
			wasmParts[name] = true
		} else if !utf8.Valid(value) {
			return DecodedUpload{}, errors.New("JavaScript and metadata parts must be UTF-8")
		}
		parts[name] = value
	}
	if len(sdkMetadata) > 0 {
		if _, exists := parts["metadata"]; exists {
			return DecodedUpload{}, errors.New("mixed JSON and SDK metadata")
		}
		parts["metadata"], err = json.Marshal(sdkMetadata)
		if err != nil || len(parts["metadata"]) > 1<<20 {
			return DecodedUpload{}, errors.New("metadata exceeds 1 MiB")
		}
	}
	metadata, err := decodeMetadata(parts["metadata"])
	if err != nil {
		return DecodedUpload{}, err
	}
	delete(parts, "metadata")

	if _, exists := parts[metadata.MainModule]; !exists {
		return DecodedUpload{}, errors.New("main_module part is missing")
	}
	if wasmParts[metadata.MainModule] {
		return DecodedUpload{}, errors.New("main_module must be JavaScript")
	}
	upload := DecodedUpload{Metadata: metadata}
	for name, content := range parts {
		file := nanoflare.WorkerFile{Name: path.Base(name), Path: name, Size: int64(len(content))}
		if wasmParts[name] {
			file.Wasm = bytes.Clone(content)
		} else {
			file.Content = string(content)
		}
		upload.Files = append(upload.Files, file)
	}
	sort.Slice(upload.Files, func(i, j int) bool { return upload.Files[i].Path < upload.Files[j].Path })
	return upload, nil
}
func CompleteUpload(upload DecodedUpload, previous *Previous, resolve Resolver, options Options) (Candidate, error) {
	metadata := upload.Metadata
	metadata.StrictInherit = options.StrictInherit
	if metadata.Assets != nil {
		if options.LoadCompletedAssets == nil {
			return Candidate{}, errors.New("completed asset uploads are unavailable")
		}
		assets, err := options.LoadCompletedAssets()
		if err != nil {
			return Candidate{}, err
		}
		copy := Previous{}
		if previous != nil {
			copy = *previous
		}
		copy.Deployment.Assets = append([]nanoflare.AssetFile{}, assets...)
		copy.Deployment.AssetConfig = nanoflare.AssetConfig{}
		if metadata.Assets.Config != nil {
			copy.Deployment.AssetConfig = *metadata.Assets.Config
		}
		for index := range copy.Deployment.Assets {
			copy.Deployment.Assets[index].Path = strings.TrimPrefix(copy.Deployment.Assets[index].Path, "/")
		}
		previous = &copy
		metadata.KeepAssets = true
	}
	if metadata.Assets == nil && metadata.KeepAssets && previous != nil && options.LoadRetainedAssets != nil {
		assets, err := options.LoadRetainedAssets()
		if err != nil {
			return Candidate{}, err
		}
		copy := *previous
		copy.Deployment = previous.Deployment
		copy.Deployment.Assets = assets
		previous = &copy
	}
	result, err := translate(metadata, previous, resolve, options.AllowDispatchNamespaces)
	if err != nil {
		return Candidate{}, err
	}
	result.Deployment.Files = nanoflare.CloneWorkerFiles(upload.Files)
	return result, nil
}
func ParseUpload(contentType string, body io.Reader, previous *Previous, resolve Resolver, options Options) (Candidate, error) {
	upload, err := DecodeUpload(contentType, body)
	if err != nil {
		return Candidate{}, err
	}
	return CompleteUpload(upload, previous, resolve, options)
}

func supportedBinding(kind string) bool {
	switch kind {
	case "plain_text", "json", "secret_text", "kv_namespace", "d1", "r2_bucket", "service", "assets":
		return true
	}
	return false
}

// Translate resolves preservation into a complete snapshot. Explicit entries
// replace retained bindings by name; inherit requires an existing exact name.
// It does not mutate the previous snapshot or expose secret values in errors.
func Translate(metadata Metadata, previous *Previous, resolve Resolver) (Candidate, error) {
	return translate(metadata, previous, resolve, false)
}

func translate(metadata Metadata, previous *Previous, resolve Resolver, allowDispatch bool) (Candidate, error) {
	result := Candidate{Secrets: map[string]string{}, KeepSecrets: map[string]string{}, Tags: []string{}}
	result.Deployment = nanoflare.DeployInput{Format: "modules", Entrypoint: metadata.MainModule, CompatibilityDate: metadata.CompatibilityDate, CompatibilityFlags: append([]string(nil), metadata.CompatibilityFlags...), Vars: map[string]json.RawMessage{}}
	prior := map[string]Binding{}
	if previous != nil {
		for _, binding := range previous.Bindings {
			binding.JSON = append(json.RawMessage(nil), binding.JSON...)
			if binding.Text != nil {
				text := *binding.Text
				binding.Text = &text
			}
			prior[binding.Name] = binding
		}
		result.Tags = append(result.Tags, previous.Tags...)
	}
	if metadata.Tags != nil {
		result.Tags = append([]string{}, (*metadata.Tags)...)
	}
	bindings := map[string]Binding{}
	retained := map[string]string{}
	for _, kind := range metadata.KeepBindings {
		// Wrangler --keep-vars always requests secret_key retention too. This
		// profile cannot create secret-key bindings; retaining an empty set is
		// safe, but never silently discard a binding from a future native profile.
		if kind == "secret_key" {
			for _, binding := range prior {
				if binding.Type == kind {
					return Candidate{}, errors.New("secret-key retention is unsupported")
				}
			}
			continue
		}
		if !supportedBinding(kind) && !(allowDispatch && kind == "dispatch_namespace") {
			return Candidate{}, errors.New("unsupported keep_bindings type")
		}
		for name, binding := range prior {
			if binding.Type == kind {
				bindings[name] = binding
				retained[name] = name
			}
		}
	}
	seen := map[string]bool{}
	for _, binding := range metadata.Bindings {
		if binding.Name == "" || len(binding.Name) > 128 || seen[binding.Name] {
			return Candidate{}, errors.New("invalid or duplicate binding name")
		}
		seen[binding.Name] = true
		if binding.Type == "inherit" {
			if binding.VersionID != "" && binding.VersionID != "latest" {
				return Candidate{}, errors.New("inherit supports only the latest uploaded revision")
			}
			oldName := binding.OldName
			if oldName == "" {
				oldName = binding.Name
			}
			copy := binding
			copy.Name = ""
			copy.Type = ""
			copy.OldName = ""
			copy.VersionID = ""
			encoded, _ := json.Marshal(copy)
			if string(encoded) != "{\"name\":\"\",\"type\":\"\"}" {
				return Candidate{}, errors.New("inherit permits only name, type, old_name and version_id")
			}
			old, ok := prior[oldName]
			if !ok {
				if metadata.StrictInherit {
					return Candidate{}, errors.New("inherited binding does not exist")
				}
				delete(bindings, binding.Name)
				delete(retained, binding.Name)
				continue
			}
			retained[binding.Name] = oldName
			old.Name = binding.Name
			binding = old
		} else {
			delete(retained, binding.Name)
		}
		bindings[binding.Name] = binding
	}
	if len(bindings) > 128 {
		return Candidate{}, errors.New("effective binding limit exceeded")
	}
	names := make([]string, 0, len(bindings))
	for name := range bindings {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		binding := bindings[name]
		if !supportedBinding(binding.Type) && !(allowDispatch && binding.Type == "dispatch_namespace") {
			return Candidate{}, errors.New("unsupported binding type")
		}
		if err := binding.validate(); err != nil {
			return Candidate{}, err
		}
		switch binding.Type {
		case "plain_text":
			result.Deployment.Vars[name], _ = json.Marshal(*binding.Text)
		case "json":
			result.Deployment.Vars[name] = append(json.RawMessage(nil), binding.JSON...)
		case "secret_text":
			if binding.Text != nil {
				result.Secrets[name] = *binding.Text
				binding.Text = nil
			} else {
				oldName, ok := retained[name]
				if !ok || prior[oldName].Type != "secret_text" {
					return Candidate{}, errors.New("secret binding requires a value or an existing secret")
				}
				result.KeepSecrets[name] = oldName
			}
		case "kv_namespace":
			result.Deployment.KVNamespaces = append(result.Deployment.KVNamespaces, nanoflare.KVBinding{Binding: name, ID: binding.NamespaceID})
		case "d1":
			// Persist the current wire spelling, including inherited legacy bindings.
			binding.DatabaseID = binding.databaseID()
			binding.ID = ""
			result.Deployment.Databases = append(result.Deployment.Databases, nanoflare.DatabaseBinding{Binding: name, DatabaseID: binding.databaseID()})
		case "dispatch_namespace":
			if resolve == nil {
				return Candidate{}, errors.New("scoped namespace resolution is required")
			}
			id, err := resolve(binding.Type, binding.Namespace)
			if err != nil || id == "" {
				return Candidate{}, errors.New("dispatch namespace was not found or is not authorized")
			}
			if source, inherited := retained[name]; inherited {
				// A renamed/recreated namespace must not redirect retained authority.
				pinned := ""
				for _, old := range previous.Deployment.DispatchNamespaces {
					if old.Binding == source {
						pinned = old.NamespaceID
					}
				}
				if pinned == "" || pinned != id {
					return Candidate{}, errors.New("retained dispatch namespace identity changed; supply an explicit binding")
				}
			}
			result.Deployment.DispatchNamespaces = append(result.Deployment.DispatchNamespaces, nanoflare.DispatchBinding{Binding: name, NamespaceID: id})
		case "r2_bucket", "service":
			if resolve == nil {
				return Candidate{}, errors.New("scoped resource resolution is required")
			}
			external := binding.BucketName
			if binding.Type == "service" {
				external = binding.Service
			}
			id, err := resolve(binding.Type, external)
			if err != nil {
				return Candidate{}, errors.New("binding resource was not found or is not authorized")
			}
			if id == "" {
				return Candidate{}, errors.New("binding resource was not found or is not authorized")
			}
			if binding.Type == "r2_bucket" {
				result.Deployment.ObjectStorageBuckets = append(result.Deployment.ObjectStorageBuckets, nanoflare.ObjectStorageBucketBinding{Binding: name, BucketID: id})
			} else {
				result.Deployment.Services = append(result.Deployment.Services, nanoflare.ServiceBinding{Binding: name, WorkerID: id})
			}
		case "assets":
			result.Deployment.AssetConfig.Binding = name
		}
		result.Bindings = append(result.Bindings, binding)
	}
	if metadata.KeepAssets {
		if previous == nil {
			return Candidate{}, errors.New("keep_assets requires a previous revision")
		}
		result.Deployment.Assets = append([]nanoflare.AssetFile(nil), previous.Deployment.Assets...)
		for index := range result.Deployment.Assets {
			result.Deployment.Assets[index].Data = append([]byte(nil), result.Deployment.Assets[index].Data...)
		}
		binding := result.Deployment.AssetConfig.Binding
		result.Deployment.AssetConfig = previous.Deployment.AssetConfig
		result.Deployment.AssetConfig.Binding = binding
	}
	if result.Deployment.AssetConfig.Binding != "" && len(result.Deployment.Assets) == 0 {
		return Candidate{}, errors.New("assets binding requires retained assets or a completed upload")
	}
	// Assets may be served automatically without exposing a named binding to
	// customer code, as used by the upstream Internal Sites template.
	return result, nil
}

func (binding Binding) validate() error {
	allowed := map[string]bool{"name": true, "type": true}
	var required string
	switch binding.Type {
	case "plain_text":
		allowed["text"] = true
		if binding.Text == nil {
			return errors.New("plain_text requires text")
		}
	case "json":
		allowed["json"] = true
		if len(binding.JSON) == 0 || !json.Valid(binding.JSON) {
			return errors.New("json binding requires valid json")
		}
	case "secret_text":
		allowed["text"] = true
	case "kv_namespace":
		allowed["namespace_id"] = true
		required = binding.NamespaceID
	case "d1":
		allowed["id"] = true
		allowed["database_id"] = true
		if binding.ID != "" && binding.DatabaseID != "" && binding.ID != binding.DatabaseID {
			return errors.New("conflicting D1 database identifiers")
		}
		required = binding.databaseID()
	case "r2_bucket":
		allowed["bucket_name"] = true
		required = binding.BucketName
	case "service":
		allowed["service"] = true
		allowed["environment"] = true
		required = binding.Service
		if binding.Environment != "" && binding.Environment != "production" {
			return errors.New("service environments are unsupported")
		}
	case "dispatch_namespace":
		allowed["namespace"] = true
		required = binding.Namespace
	case "assets":
	default:
		return errors.New("unsupported binding type")
	}
	if (binding.Type == "kv_namespace" || binding.Type == "d1" || binding.Type == "r2_bucket" || binding.Type == "service" || binding.Type == "dispatch_namespace") && required == "" {
		return errors.New("binding resource identifier is required")
	}
	encoded, _ := json.Marshal(binding)
	var fields map[string]json.RawMessage
	_ = json.Unmarshal(encoded, &fields)
	for field := range fields {
		if !allowed[field] {
			return fmt.Errorf("field %s is unsupported for binding type %s", field, binding.Type)
		}
	}
	return nil
}

// databaseID accepts retained snapshots and clients using the deprecated id field.
func (binding Binding) databaseID() string {
	if binding.DatabaseID != "" {
		return binding.DatabaseID
	}
	return binding.ID
}
