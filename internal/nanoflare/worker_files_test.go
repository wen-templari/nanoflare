package nanoflare

import (
	"bytes"
	"testing"
)

func TestWasmDeploymentFilesAndFrozenBytes(t *testing.T) {
	wasm := []byte{0, 97, 115, 109, 1, 0, 0, 0, 0, 4, 1, 'x', 255, 254}
	files := []WorkerFile{{Path: "worker.js", Content: "export default {}"}, {Path: "lib.wasm", Wasm: wasm, Size: 999}}
	normalized, entry, err := deploymentFiles(files, "worker.js")
	if err != nil || entry != "worker.js" || normalized[1].Size != int64(len(wasm)) {
		t.Fatal(normalized, entry, err)
	}
	snapshot, err := FreezeExecution(ActiveDeployment{App: App{ID: "wasm"}, Deployment: Deployment{ID: "r", Files: normalized}})
	if err != nil {
		t.Fatal(err)
	}
	id := snapshot.ID()
	changed := snapshot.ActiveDeployment()
	changed.Deployment.Files[1].Wasm[len(changed.Deployment.Files[1].Wasm)-1] ^= 1
	different, err := FreezeExecution(changed)
	if err != nil || different.ID() == id {
		t.Fatal("binary bytes missing from execution identity", err)
	}
	wasm[8] = 42
	if normalized[1].Wasm[8] != 0 {
		t.Fatal("deployment retained caller bytes")
	}
	normalized[1].Wasm[8] = 43
	detached := snapshot.ActiveDeployment()
	detached.Deployment.Files[1].Wasm[8] = 44
	if snapshot.ID() != id || snapshot.ActiveDeployment().Deployment.Files[1].Wasm[8] != 0 {
		t.Fatal("frozen Wasm mutated")
	}
	for name, bad := range map[string][]WorkerFile{
		"empty":      {{Path: "lib.wasm", Wasm: []byte{}}},
		"header":     {{Path: "lib.wasm", Wasm: []byte("not-wasm")}},
		"mixed":      {{Path: "lib.wasm", Wasm: files[1].Wasm, Content: "export default {}"}},
		"entrypoint": {{Path: "lib.wasm", Wasm: files[1].Wasm}},
	} {
		t.Run(name, func(t *testing.T) {
			if _, _, err := deploymentFiles(bad, "lib.wasm"); err == nil {
				t.Fatal("invalid binary accepted")
			}
		})
	}
	oversized := bytes.Repeat([]byte{0}, maxWorkerBundleSize+1)
	copy(oversized, []byte{0, 97, 115, 109, 1, 0, 0, 0})
	if _, _, err := deploymentFiles([]WorkerFile{{Path: "worker.js", Content: "export default {}"}, {Path: "big.wasm", Wasm: oversized}}, "worker.js"); err == nil {
		t.Fatal("binary bundle limit bypassed")
	}
}

func TestBinaryWorkerObjectStorageRoundTrip(t *testing.T) {
	repo := newObjectBackedRepo()
	objects := newMemoryObjectStore()
	service := NewServiceWithObjects(repo, &recordingWriter{}, objects)
	app, err := service.CreateApp(CreateAppInput{Name: "Binary", Hostname: "binary.example.test"})
	if err != nil {
		t.Fatal(err)
	}
	wasm := []byte{0, 97, 115, 109, 1, 0, 0, 0, 0, 4, 1, 'x', 255, 254}
	_, err = service.Deploy(app.ID, DeployInput{Format: "modules", Entrypoint: "worker.js", CompatibilityDate: "2025-12-10", Files: []WorkerFile{{Path: "worker.js", Content: "export default {}"}, {Path: "lib.wasm", Wasm: wasm}}})
	if err != nil {
		t.Fatal(err)
	}
	wasm[0] = 255
	files, err := service.WorkerFiles(app.ID)
	if err != nil || len(files) != 2 || files[1].Wasm[0] != 0 || files[1].Wasm[12] != 255 {
		t.Fatal(files, err)
	}
	files[1].Wasm[0] = 254
	again, err := service.WorkerFiles(app.ID)
	if err != nil || again[1].Wasm[0] != 0 {
		t.Fatal("binary output aliases stored bytes", again, err)
	}
	for _, file := range []WorkerFile{{Path: "empty.wasm", Wasm: []byte{}}, {Path: "mixed.wasm", Wasm: again[1].Wasm, Content: "mixed"}} {
		if _, err := FreezeExecution(ActiveDeployment{App: App{ID: "a"}, Deployment: Deployment{ID: "d", Files: []WorkerFile{{Path: "worker.js"}, file}}}); err == nil {
			t.Fatal("ambiguous frozen binary accepted")
		}
	}
}
