package cfcompat

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"unicode/utf8"
)

type SecretTextValue struct {
	Name string `json:"name"`
	Text string `json:"-"`
}

// A nil entry deletes that secret. Values are deliberately excluded from public
// serialization; HTTP retry identity uses the separately validated original JSON.
func DecodeSecretPatch(data []byte) (map[string]*SecretTextValue, error) {
	var input struct {
		Secrets map[string]json.RawMessage `json:"secrets"`
	}
	if err := DecodeObject(data, &input, "secrets"); err != nil {
		return nil, err
	}
	var fields map[string]json.RawMessage
	_ = json.Unmarshal(data, &fields)
	if raw, exists := fields["secrets"]; exists && bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return nil, errors.New("secrets must be an object")
	}
	if len(input.Secrets) > 128 {
		return nil, errors.New("secret patch exceeds 128 entries")
	}
	result := map[string]*SecretTextValue{}
	for name, raw := range input.Secrets {
		if name == "" || len(name) > 128 || !utf8.ValidString(name) || strings.TrimSpace(name) != name {
			return nil, errors.New("invalid secret name")
		}
		if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
			result[name] = nil
			continue
		}
		var value struct {
			Name string  `json:"name"`
			Type string  `json:"type"`
			Text *string `json:"text"`
		}
		if err := DecodeObject(raw, &value, "name", "type", "text"); err != nil {
			return nil, err
		}
		if value.Name != name || value.Type != "secret_text" || value.Text == nil || !utf8.ValidString(*value.Text) || len(*value.Text) > 1<<20 {
			return nil, errors.New("each secret requires a matching name and a secret_text value")
		}
		result[name] = &SecretTextValue{Name: name, Text: *value.Text}
	}
	return result, nil
}
