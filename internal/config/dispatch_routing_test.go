package config

import (
	"os/exec"
	"strings"
	"testing"

	"github.com/clas/nanoflare/internal/nanoflare"
)

func TestDynamicAssetRouting(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed")
	}
	for _, kind := range []string{"object", "class", "asset-only"} {
		t.Run(kind, func(t *testing.T) {
			d := nanoflare.Deployment{Entrypoint: "worker.js", Assets: []nanoflare.AssetFile{{Path: "/hello.txt"}}, AssetConfig: nanoflare.AssetConfig{RunWorkerFirst: nanoflare.RunWorkerFirst{"/api/*", "!/api/docs/*"}}}
			source := strings.SplitN(DynamicEntrypoint(d), "\n", 3)[2]
			source = strings.Replace(source, "export default handler;", "", 1)
			worker := `const userWorker={fetch:()=>new Response("worker")};`
			if kind == "class" {
				worker = `const userWorker=class {constructor(ctx,env){this.ctx=ctx;this.env=env;} fetch(){return new Response("worker");}};`
			} else if kind == "asset-only" {
				worker = `const userWorker={};`
			}
			script := `import assert from "node:assert/strict";
const dynamicEnv=env=>env, withInvocation=(ctx,run)=>run();
` + worker + "\n" + source + `
const env={ASSETS:{fetch:async request=>new Response("asset",{status:new URL(request.url).pathname==="/missing"?404:200})}};
const instance=typeof userWorker==="function"?new handler({},env):{fetch:r=>handler.fetch(r,env,{})};
assert.equal(await(await instance.fetch(new Request("https://example/hello.txt"))).text(),"asset");
assert.equal(await(await instance.fetch(new Request("https://example/api/docs/readme"))).text(),"asset");
` + func() string {
				if kind == "asset-only" {
					return `assert.equal((await instance.fetch(new Request("https://example/missing"))).status,404);`
				}
				return `for(const [path,method] of [["/missing","GET"],["/missing","HEAD"],["/hello.txt","POST"],["/api/data","GET"]]) assert.equal(await(await instance.fetch(new Request("https://example"+path,{method}))).text(),"worker");`
			}()
			command := exec.Command(node, "--input-type=module", "-e", script)
			if output, err := command.CombinedOutput(); err != nil {
				t.Fatalf("routing: %v\n%s", err, output)
			}
		})
	}
}
