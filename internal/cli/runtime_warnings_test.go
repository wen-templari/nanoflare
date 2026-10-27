package cli

import (
	"bytes"
	"github.com/clas/nanoflare/internal/nanoflare"
	"strings"
	"testing"
)

func TestPrivateRuntimeWarningsRecognizeAPIs(t *testing.T) {
	for _, tc := range []struct {
		source   string
		warnings int
	}{
		{`fetch(url,{cf:{cacheTtl:60}});caches.default.match(url);`, 2},
		{`fetch(new URL(url), {headers: {x:'y'}, cf:{}});globalThis.caches.open('x');`, 2},
		{`// fetch(url,{cf:{}}); caches.default
const text="caches.default fetch(url,{cf:{}})"; const regex=/fetch(url,{cf:{}})/; fetch(url,{signal:AbortSignal.timeout(10)});`, 0},
		{`other.fetch(url,{cf:{}});other.caches.default;`, 0},
		{`const caches={default:1};function fetch(){};fetch(url,{cf:{}});caches.default;`, 0},
	} {
		var output bytes.Buffer
		warnPrivateRuntime([]nanoflare.WorkerFile{{Path: "worker.js", Content: tc.source}}, &output)
		if got := strings.Count(output.String(), "warning:"); got != tc.warnings {
			t.Errorf("%s: got %d warnings, want %d: %s", tc.source, got, tc.warnings, &output)
		}
	}
}
