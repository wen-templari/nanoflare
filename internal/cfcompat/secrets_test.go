package cfcompat

import (
	"bytes"
	"encoding/json"
	"testing"
)

func TestSecretPatchNeverSerializesValues(t *testing.T) {
	patch, err := DecodeSecretPatch([]byte(`{"secrets":{"ADD":{"name":"ADD","type":"secret_text","text":"never-serialize-this"},"DROP":null}}`))
	if err != nil {
		t.Fatal(err)
	}
	if patch["ADD"].Text != "never-serialize-this" || patch["DROP"] != nil {
		t.Fatal("secret patch semantics changed")
	}
	encoded, err := json.Marshal(patch)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(encoded, []byte("never-serialize-this")) {
		t.Fatal("decoded patch leaked secret values")
	}
}
