package cfcompat

import (
	"strings"
	"testing"
)

func TestOutboundBindingTranslationAndRetention(t *testing.T) {
	metadata := `{"main_module":"worker.js","bindings":[{"name":"D","type":"dispatch_namespace","namespace":"sites","outbound":{"worker":{"service":"policy","environment":"production","entrypoint":"Policy"},"params":[{"name":"context"}]}}]}`
	resolve := func(kind, name string) (string, error) {
		if kind == "dispatch_namespace" {
			return "namespace-id", nil
		}
		if kind != "service" || name != "policy" {
			t.Fatalf("unexpected resolution %s %s", kind, name)
		}
		return "policy-id", nil
	}
	candidate, err := parseFixture(t, metadata, nil, resolve, Options{AllowDispatchNamespaces: true})
	if err != nil {
		t.Fatal(err)
	}
	config := candidate.Deployment.DispatchNamespaces[0].Outbound
	if config == nil || config.WorkerID != "policy-id" || config.Entrypoint != "Policy" || len(config.Parameters) != 1 || config.Parameters[0] != "context" {
		t.Fatalf("incorrect outbound translation: %+v", config)
	}
	previous := &Previous{Bindings: candidate.Bindings, Deployment: candidate.Deployment}
	for _, next := range []Metadata{
		{MainModule: "worker.js", KeepBindings: []string{"dispatch_namespace"}},
		{MainModule: "worker.js", Bindings: []Binding{{Name: "RENAMED", Type: "inherit", OldName: "D"}}},
	} {
		retained, err := translate(next, previous, resolve, true)
		if err != nil || (retained.Deployment.DispatchNamespaces[0].Outbound.WorkerID != "policy-id" || retained.Deployment.DispatchNamespaces[0].Outbound.Entrypoint != "Policy") {
			t.Fatalf("outbound retention: %+v %v", retained, err)
		}
		if _, err = translate(next, previous, func(kind, name string) (string, error) {
			if kind == "service" {
				return "replacement-policy", nil
			}
			return resolve(kind, name)
		}, true); err == nil {
			t.Fatal("retention redirected outbound authority")
		}
	}
	for _, replacement := range []string{
		`{"worker":{"service":"policy","Service":"other"}}`,
		`{"worker":{"service":"policy"},"Params":[]}`,
		`{"worker":{"service":"policy"},"params":[{"name":"context"},{"name":"context"}]}`,
		`null`,
	} {
		invalid := strings.Replace(metadata, `{"worker":{"service":"policy","environment":"production","entrypoint":"Policy"},"params":[{"name":"context"}]}`, replacement, 1)
		if _, err := parseFixture(t, invalid, nil, resolve, Options{AllowDispatchNamespaces: true}); err == nil {
			t.Fatalf("accepted invalid outbound: %s", replacement)
		}
	}
}

func TestOutboundEnvironmentTranslation(t *testing.T) {
	metadata := `{"main_module":"worker.js","bindings":[{"name":"D","type":"dispatch_namespace","namespace":"sites","outbound":{"worker":{"service":"policy","environment":"staging","entrypoint":"Policy"}}}]}`
	resolve := func(kind, name string) (string, error) {
		switch kind {
		case "dispatch_namespace":
			return "ns", nil
		case "service_environment":
			if name != `["policy","staging"]` {
				t.Fatalf("selector=%s", name)
			}
			return "staging-child-id", nil
		}
		t.Fatalf("unexpected selector %s %s", kind, name)
		return "", nil
	}
	candidate, err := parseFixture(t, metadata, nil, resolve, Options{AllowDispatchNamespaces: true})
	if err != nil {
		t.Fatal(err)
	}
	binding := candidate.Deployment.DispatchNamespaces[0].Outbound
	if binding.WorkerID != "staging-child-id" || binding.Entrypoint != "Policy" {
		t.Fatalf("binding=%+v", binding)
	}
	retained, err := translate(Metadata{MainModule: "worker.js", KeepBindings: []string{"dispatch_namespace"}}, &Previous{Bindings: candidate.Bindings, Deployment: candidate.Deployment}, resolve, true)
	if err != nil || retained.Deployment.DispatchNamespaces[0].Outbound.WorkerID != "staging-child-id" {
		t.Fatalf("retained=%+v err=%v", retained, err)
	}
}
