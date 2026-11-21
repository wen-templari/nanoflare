package cfcompat

import "testing"

func TestWorkerLoaderUploadAndKeepBindings(t *testing.T) {
	first, err := parseFixture(t, `{"main_module":"worker.js","bindings":[{"name":"LOADER","type":"worker_loader"}]}`, nil, nil, Options{})
	if err != nil {
		t.Fatal(err)
	}
	previous := &Previous{Bindings: first.Bindings, Deployment: first.Deployment}
	next, err := parseFixture(t, `{"main_module":"worker.js","keep_bindings":["worker_loader"]}`, previous, nil, Options{})
	if err != nil {
		t.Fatal(err)
	}
	for _, candidate := range []Candidate{first, next} {
		loaders := candidate.Deployment.WorkerLoaders
		if len(loaders) != 1 || loaders[0].Binding != "LOADER" {
			t.Fatalf("worker loaders = %#v", loaders)
		}
	}
}
