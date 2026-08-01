package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestOpenAPIListsEveryPublicOperation(t *testing.T) {
	data, err := OpenAPIJSON()
	if err != nil {
		t.Fatal(err)
	}
	var spec struct {
		OpenAPI string                                `json:"openapi"`
		Paths   map[string]map[string]json.RawMessage `json:"paths"`
	}
	if err := json.Unmarshal(data, &spec); err != nil {
		t.Fatal(err)
	}
	if spec.OpenAPI != "3.1.0" {
		t.Fatalf("OpenAPI version = %q", spec.OpenAPI)
	}
	for _, operation := range publicOperations() {
		path := strings.ReplaceAll(operation.path, "...", "")
		if _, ok := spec.Paths[path][strings.ToLower(operation.method)]; !ok {
			t.Errorf("missing %s %s", operation.method, path)
		}
	}
}

func TestOpenAPISchemaAndDocsAreServed(t *testing.T) {
	server := NewServer(nil)
	for _, path := range []string{"/openapi.json", "/openapi-3.0.json", "/docs"} {
		recorder := httptest.NewRecorder()
		server.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, path, nil))
		if recorder.Code != http.StatusOK {
			t.Errorf("GET %s status = %d", path, recorder.Code)
		}
	}
}

func TestOpenAPISuccessResponsesAndWorkerOutputQueryAreTyped(t *testing.T) {
	data, err := OpenAPIJSON()
	if err != nil {
		t.Fatal(err)
	}
	var spec struct {
		Paths map[string]map[string]struct {
			Parameters []struct {
				In   string `json:"in"`
				Name string `json:"name"`
			} `json:"parameters"`
			Responses map[string]struct {
				Content map[string]struct {
					Schema json.RawMessage `json:"schema"`
				} `json:"content"`
			} `json:"responses"`
		} `json:"paths"`
	}
	if err := json.Unmarshal(data, &spec); err != nil {
		t.Fatal(err)
	}
	for path, methods := range spec.Paths {
		for method, operation := range methods {
			for status, response := range operation.Responses {
				if !strings.HasPrefix(status, "2") {
					continue
				}
				jsonContent, ok := response.Content["application/json"]
				if !ok {
					continue
				}
				var schema map[string]json.RawMessage
				if err := json.Unmarshal(jsonContent.Schema, &schema); err != nil {
					t.Fatal(err)
				}
				var kind string
				var properties map[string]json.RawMessage
				_ = json.Unmarshal(schema["type"], &kind)
				_ = json.Unmarshal(schema["properties"], &properties)
				if len(schema["$ref"]) == 0 && kind == "object" && len(properties) == 0 {
					t.Errorf("%s %s %s has an untyped JSON success response", method, path, status)
				}
			}
		}
	}
	for _, parameter := range spec.Paths["/v1/organizations/{orgID}/workers/{workerID}/output"]["get"].Parameters {
		if parameter.In == "query" && parameter.Name == "limit" {
			return
		}
	}
	t.Error("worker output query parameter limit is missing")
}
