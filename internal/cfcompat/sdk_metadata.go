package cfcompat

import (
	"errors"
	"strings"
)

// Decode only forms whose type and boundaries survive SDK serialization.
// Nested object arrays lose element boundaries and must use JSON metadata.
func addSDKMetadata(fields map[string]any, name, value string) error {
	key := strings.TrimPrefix(name, "metadata[")
	switch key {
	case "main_module]", "compatibility_date]":
		key = strings.TrimSuffix(key, "]")
		if _, exists := fields[key]; exists {
			return errors.New("duplicate SDK metadata scalar")
		}
		fields[key] = value
	case "compatibility_flags][]", "keep_bindings][]", "tags][]":
		key = strings.TrimSuffix(key, "][]")
		list, _ := fields[key].([]string)
		fields[key] = append(list, value)
	case "keep_assets]":
		if _, exists := fields["keep_assets"]; exists || (value != "true" && value != "false") {
			return errors.New("invalid SDK keep_assets")
		}
		fields["keep_assets"] = value == "true"
	case "assets][jwt]":
		assets := sdkObject(fields, "assets")
		if _, exists := assets["jwt"]; exists {
			return errors.New("duplicate SDK asset token")
		}
		assets["jwt"] = value
	case "assets][config][html_handling]", "assets][config][not_found_handling]", "assets][config][run_worker_first]", "assets][config][run_worker_first][]":
		config := sdkObject(sdkObject(fields, "assets"), "config")
		field := strings.TrimSuffix(strings.TrimPrefix(key, "assets][config]["), "]")
		if key == "assets][config][run_worker_first][]" {
			if existing, exists := config["run_worker_first"]; exists {
				if _, ok := existing.([]string); !ok {
					return errors.New("mixed SDK worker-first forms")
				}
			}
			routes, _ := config["run_worker_first"].([]string)
			config["run_worker_first"] = append(routes, value)
		} else {
			if _, exists := config[field]; exists {
				return errors.New("duplicate SDK asset configuration")
			}
			if field == "run_worker_first" {
				if value != "true" && value != "false" {
					return errors.New("invalid SDK worker-first boolean")
				}
				config[field] = value == "true"
			} else {
				config[field] = value
			}
		}
	default:
		return errors.New("unsupported or ambiguous SDK metadata field; use JSON metadata")
	}
	return nil
}

func sdkObject(parent map[string]any, key string) map[string]any {
	object, ok := parent[key].(map[string]any)
	if !ok {
		object = map[string]any{}
		parent[key] = object
	}
	return object
}
