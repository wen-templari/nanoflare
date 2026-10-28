package config

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/clas/nanoflare/internal/nanoflare"
)

func TestDNSImportRewritePreservesUploadedSource(t *testing.T) {
	umd := `(function(g,f){if(typeof module==='object')module.exports=f();else g.Lib=f()})(globalThis,function(){ const value=1; let next=2; return {value,next}; });`
	for _, source := range []string{umd, "// dns data\n" + umd, "const type = 'dns';\n" + umd} {
		got, err := rewriteDNSImports(source, true)
		if err != nil || got != source {
			t.Fatalf("changed UMD: %v\n%s", err, got)
		}
	}
	source := "// heading\nimport a from 'node:dns'; import b from 'node:dns';\nconst value = 'dns'; let next = 2;\nconst pattern = /import('dns')/;\nconst template = `import('dns')`;\nexport {lookup} from \"dns/promises\";\nconst promise = import('dns');\n" + umd
	want := strings.ReplaceAll(source, "from 'node:dns'", "from 'nanoflare-internal:dns'")
	want = strings.Replace(want, `from "dns/promises"`, `from "nanoflare-internal:dns/promises"`, 1)
	want = strings.Replace(want, "const promise = import('dns')", "const promise = import('nanoflare-internal:dns')", 1)
	got, err := rewriteDNSImports(source, true)
	if err != nil || got != want {
		t.Fatalf("range rewrite: %v\ngot: %s\nwant: %s", err, got, want)
	}
	if strings.Count(got, "\n") != strings.Count(source, "\n") {
		t.Fatal("changed line numbers")
	}
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed")
	}
	got, err = rewriteDNSImports(umd, true)
	if err != nil {
		t.Fatal(err)
	}
	if output, err := exec.Command(node, "--input-type=module", "-e", got+`; if (globalThis.Lib?.value !== 1) throw new Error('UMD global lost');`).CombinedOutput(); err != nil {
		t.Fatalf("UMD: %v %s", err, output)
	}
}

func TestDNSImportRewriteErrorsAreNotNativeFallback(t *testing.T) {
	const source = `import dns from 'node:dns'; export default <div/>;`
	if _, err := rewriteDNSImports(source, true); err == nil {
		t.Fatal("accepted JSX and silently left native DNS")
	}
	d := nanoflare.Deployment{Format: "modules", Entrypoint: "worker.js", CompatibilityDate: "2026-09-11", Files: []nanoflare.WorkerFile{{Path: "worker.js", Content: source}}}
	if _, err := NamespaceWorkerModules(d); err == nil {
		t.Fatal("namespace compile accepted failed rewrite")
	}
	if _, err := json.Marshal(SnapshotWorkerCode(nanoflare.ActiveDeployment{Deployment: d}, WorkerdOptions{DNSAddr: "127.0.0.1:1"})); err == nil {
		t.Fatal("loader serialized failed rewrite")
	}
	writer := NewRuntimeWriter(filepath.Join(t.TempDir(), "workerd.capnp"), nil)
	writer.SetWorkerdDNSAddr("127.0.0.1:1")
	if err := writer.WriteWorkerd(filepath.Join(t.TempDir(), "workerd.capnp"), []nanoflare.ActiveDeployment{{Deployment: d}}); err == nil {
		t.Fatal("static publication accepted failed rewrite")
	}
}

func TestDNSImportRewriteTemplateSpecifier(t *testing.T) {
	source := "const promise = import(`node:dns`); const data = `dns`;"
	want := "const promise = import(`nanoflare-internal:dns`); const data = `dns`;"
	got, err := rewriteDNSImports(source, true)
	if err != nil || got != want {
		t.Fatalf("template specifier: %v %s", err, got)
	}
}

func TestDNSImportRewriteEscapedSpecifiers(t *testing.T) {
	source := "import a from 'node:d\\x6es';\nimport b from 'node:d\\\nns';\n"
	got, err := rewriteDNSImports(source, true)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(got, "nanoflare-internal:dns") != 2 || strings.Count(got, "\n") != strings.Count(source, "\n") {
		t.Fatalf("escaped specifiers: %q", got)
	}
}

func TestDNSImportRewriteCaughtAndConditionalLookups(t *testing.T) {
	for _, source := range []string{
		"const marker = 1;\u2028import dns from 'node:dns';\u2029",
		"const marker = 1;\rimport dns from 'node:dns';\r",
		`try { require('node:dns'); } catch {}`,
		`import('node:dns').catch(() => {});`,
		`if (globalThis.enabled) { import('node:dns'); }`,
		`function connect(){ return require('node:dns'); }`,
		"const marker = '汉字';\r\nimport dns from 'node:dns';\r\n",
	} {
		want := strings.Replace(source, "'node:dns'", "'nanoflare-internal:dns'", 1)
		got, err := rewriteDNSImports(source, true)
		if err != nil || got != want {
			t.Fatalf("lookup not rewritten: %v\ngot %s\nwant %s", err, got, want)
		}
	}
}

func TestDNSImportRewriteSupportsModuleSyntax(t *testing.T) {
	for _, source := range []string{
		`import dns from 'node:dns'; await Promise.resolve(); export const value = 1;`,
		`const dns = await import('node:dns');`,
		`import source wasm from './lib.wasm'; import dns from 'node:dns';`,
	} {
		want := strings.ReplaceAll(source, "'node:dns'", "'nanoflare-internal:dns'")
		got, err := rewriteDNSImports(source, true)
		if err != nil || got != want {
			t.Fatalf("valid module rejected: %v\n%s", err, got)
		}
	}
}

func TestDNSRewriteCacheKeepsWorkingSetOver256Modules(t *testing.T) {
	const source = `import dns from 'node:dns'; export default dns;`
	for i := 0; i < 300; i++ {
		if _, err := rewriteDNSImportsForModule(source, true, fmt.Sprintf("module-%d.js", i)); err != nil {
			t.Fatal(err)
		}
	}
	dnsRewriteCache.Lock()
	defer dnsRewriteCache.Unlock()
	for i := 0; i < 300; i++ {
		key := dnsRewriteKey{sha256.Sum256([]byte(source)), fmt.Sprintf("module-%d.js", i), true}
		if _, ok := dnsRewriteCache.entries[key]; !ok {
			t.Fatalf("working set lost module %d", i)
		}
	}
}

func TestRuntimeValidationDoesNotCompileUnrelatedRevisions(t *testing.T) {
	writer := NewRuntimeWriter(filepath.Join(t.TempDir(), "workerd.capnp"), nil)
	writer.SetWorkerdDNSAddr("127.0.0.1:1")
	bad := nanoflare.ActiveDeployment{Deployment: nanoflare.Deployment{CompatibilityDate: "2026-09-11", Files: []nanoflare.WorkerFile{{Path: "worker.js", Content: `import dns from 'node:dns'; export default <div/>;`}}}}
	if err := writer.ValidateRuntime([]nanoflare.ActiveDeployment{bad}); err != nil {
		t.Fatalf("global validation blocked per-revision isolation: %v", err)
	}
}
