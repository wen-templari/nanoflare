package cfcompat

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"mime/multipart"
	"strings"
	"unicode/utf8"
)

// DecodeSettings reads exactly one JSON settings part. Multipart boundaries and
// header order do not become part of the normalized mutation identity.
func DecodeSettings(contentType string, body io.Reader) (json.RawMessage, error) {
	data, err := io.ReadAll(io.LimitReader(body, (1<<20)+(64<<10)+1))
	if err != nil {
		return nil, err
	}
	if len(data) > (1<<20)+(64<<10) {
		return nil, errors.New("settings upload exceeds its limit")
	}
	kind, params, err := mime.ParseMediaType(contentType)
	if err != nil || kind != "multipart/form-data" || params["boundary"] == "" {
		return nil, errors.New("settings require multipart/form-data")
	}
	reader := multipart.NewReader(bytes.NewReader(data), params["boundary"])
	part, err := reader.NextRawPart()
	if err == nil && strings.HasPrefix(part.FormName(), "settings[") {
		return decodeSDKSettings(reader, part)
	}
	if err != nil || part.FormName() != "settings" {
		return nil, errors.New("one settings part is required")
	}
	if len(part.Header.Values("Content-Disposition")) != 1 || len(part.Header.Values("Content-Type")) > 1 || part.Header.Get("Content-Transfer-Encoding") != "" || part.Header.Get("Content-Encoding") != "" {
		return nil, errors.New("ambiguous or encoded settings parts are unsupported")
	}
	if content := part.Header.Get("Content-Type"); content != "" {
		kind, _, err := mime.ParseMediaType(content)
		if err != nil || kind != "application/json" {
			return nil, errors.New("settings part must contain JSON")
		}
	}
	settings, err := io.ReadAll(part)
	if err != nil {
		return nil, err
	}
	if len(settings) > 1<<20 {
		return nil, errors.New("settings metadata exceeds 1 MiB")
	}
	if _, err := reader.NextRawPart(); err != io.EOF {
		return nil, errors.New("only one settings part is supported")
	}
	if _, err := decodeSettingsPatch(settings); err != nil {
		return nil, err
	}
	return settings, nil
}

// SDK 7 flattens settings into bracketed fields. Only scalar and scalar-list
// fields have lossless boundaries here; binding object arrays need an explicit
// JSON settings part until their wire representation can be disambiguated.
func decodeSDKSettings(reader *multipart.Reader, part *multipart.Part) (json.RawMessage, error) {
	fields := map[string]any{}
	for count := 0; ; count++ {
		if count >= 256 || len(part.Header.Values("Content-Disposition")) != 1 || len(part.Header.Values("Content-Type")) != 0 || part.FileName() != "" || part.Header.Get("Content-Transfer-Encoding") != "" || part.Header.Get("Content-Encoding") != "" {
			return nil, errors.New("invalid SDK settings field")
		}
		value, err := io.ReadAll(part)
		if err != nil || !utf8.Valid(value) {
			return nil, errors.New("invalid SDK settings value")
		}
		name := part.FormName()
		switch name {
		case "settings[compatibility_date]":
			if _, exists := fields["compatibility_date"]; exists {
				return nil, errors.New("duplicate SDK settings field")
			}
			fields["compatibility_date"] = string(value)
		case "settings[tags][]", "settings[compatibility_flags][]":
			key := strings.TrimSuffix(strings.TrimPrefix(name, "settings["), "][]")
			values, _ := fields[key].([]string)
			fields[key] = append(values, string(value))
		default:
			return nil, errors.New("unsupported or ambiguous SDK settings field; use a JSON settings part")
		}
		part, err = reader.NextRawPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, errors.New("invalid SDK settings multipart")
		}
	}
	data, err := json.Marshal(fields)
	if err != nil || len(data) > 1<<20 {
		return nil, errors.New("settings metadata exceeds 1 MiB")
	}
	if _, err := decodeSettingsPatch(data); err != nil {
		return nil, err
	}
	return data, nil
}

type settingsPatch struct {
	Bindings           *[]Binding `json:"bindings,omitempty"`
	CompatibilityDate  *string    `json:"compatibility_date,omitempty"`
	CompatibilityFlags *[]string  `json:"compatibility_flags,omitempty"`
	Tags               *[]string  `json:"tags,omitempty"`
}

func decodeSettingsPatch(data []byte) (settingsPatch, error) {
	var patch settingsPatch
	if err := DecodeObject(data, &patch, "bindings", "compatibility_date", "compatibility_flags", "tags"); err != nil {
		return patch, err
	}
	var fields map[string]json.RawMessage
	_ = json.Unmarshal(data, &fields)
	for _, value := range fields {
		if bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return patch, errors.New("settings fields cannot be null; use an empty array to clear a list")
		}
	}
	var bindings []map[string]json.RawMessage
	_ = json.Unmarshal(fields["bindings"], &bindings)
	for _, binding := range bindings {
		if err := exactFields(binding, "name", "type", "text", "json", "namespace_id", "id", "database_id", "bucket_name", "service", "environment", "old_name", "version_id"); err != nil {
			return patch, err
		}
	}
	if patch.Bindings != nil {
		for _, binding := range *patch.Bindings {
			if binding.Type != "inherit" {
				if err := binding.validate(); err != nil {
					return patch, err
				}
			}
		}
	}
	if patch.Tags != nil {
		if err := ValidateTags(*patch.Tags); err != nil {
			return patch, err
		}
	}
	return patch, nil
}

// ApplySettings preserves omitted fields and replaces explicitly supplied lists.
// Code and assets stay pinned to the selected latest uploaded snapshot.
func ApplySettings(data []byte, previous *Previous, resolve Resolver) (Candidate, error) {
	if previous == nil {
		return Candidate{}, errors.New("settings require an uploaded revision")
	}
	if previous.Deployment.Format != "modules" || len(previous.Deployment.DispatchNamespaces) > 0 {
		return Candidate{}, errors.New("this native configuration requires the native API")
	}
	patch, err := decodeSettingsPatch(data)
	if err != nil {
		return Candidate{}, err
	}
	metadata := Metadata{MainModule: previous.Deployment.Entrypoint, CompatibilityDate: previous.Deployment.CompatibilityDate, CompatibilityFlags: previous.Deployment.CompatibilityFlags, KeepAssets: true, Tags: patch.Tags, StrictInherit: true}
	if patch.Bindings == nil {
		metadata.KeepBindings = []string{"plain_text", "json", "secret_text", "kv_namespace", "d1", "r2_bucket", "service", "assets"}
	} else {
		metadata.Bindings = *patch.Bindings
	}
	if patch.CompatibilityDate != nil {
		metadata.CompatibilityDate = *patch.CompatibilityDate
	}
	if patch.CompatibilityFlags != nil {
		metadata.CompatibilityFlags = *patch.CompatibilityFlags
	}
	result, err := Translate(metadata, previous, resolve)
	if err != nil {
		return Candidate{}, err
	}
	if len(previous.Deployment.Assets) > 0 && result.Deployment.AssetConfig.Binding == "" {
		return Candidate{}, errors.New("retained assets require an explicit assets binding in this native profile")
	}
	result.Deployment.Files = previous.Deployment.Files
	result.Deployment.DNS = previous.Deployment.DNS
	result.Deployment.Triggers = previous.Deployment.Triggers
	return result, nil
}

func ValidateTags(tags []string) error {
	if len(tags) > 8 {
		return errors.New("tag limit exceeded")
	}
	seen := map[string]bool{}
	for _, tag := range tags {
		if tag == "" || len(tag) > 256 || !utf8.ValidString(tag) || seen[tag] {
			return errors.New("invalid or duplicate tag")
		}
		seen[tag] = true
	}
	return nil
}
func DecodeTags(data []byte) ([]string, error) {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 || trimmed[0] != '[' || !utf8.Valid(data) {
		return nil, errors.New("tags require a UTF-8 JSON array")
	}
	var tags []string
	if err := strictJSON(data, &tags); err != nil {
		return nil, err
	}
	if err := ValidateTags(tags); err != nil {
		return nil, err
	}
	return tags, nil
}
