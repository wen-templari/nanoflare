package config

import (
	"encoding/json"
	"net/http"
	"os/exec"
	"strings"
	"testing"

	"github.com/clas/nanoflare/internal/dnsresolver"
)

func TestDoHFetchHookPreservesFetchSemantics(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is not installed")
	}
	// Execute the production hook with instrumented native fetch and binding.
	// The same cases go through the actual Go matcher and production JS hook.
	type matchCase struct {
		Method, URL string
		Intercepted bool
	}
	var cases []matchCase
	for _, value := range []struct{ method, url string }{
		{"GET", "https://cloudflare-dns.com/dns-query?name=private&type=A"},
		{"GET", "https://CLOUDFLARE-DNS.COM./dns-query?name=private&type=TXT"},
		{"GET", "https://cloudflare-dns.com.:443/dns-query?name=private&type=MX"},
		{"GET", "https://cloudflare-dns.com:444/dns-query?name=private&type=A"},
		{"GET", "https://cloudflare-dns.com/dns-query?dns=AAAB"},
		{"GET", "https://cloudflare-dns.com/dns-query"},
		{"GET", "https://cloudflare-dns.com/dns-query?name="},
		{"GET", "https://cloudflare-dns.com/%64ns-query?name=private"},
		{"GET", "https://cloudflare-dns.com.attacker.test/dns-query?name=private"},
		{"GET", "http://cloudflare-dns.com/dns-query?name=private"},
		{"POST", "https://cloudflare-dns.com/dns-query?name=private"},
	} {
		r, err := http.NewRequest(value.method, value.url, nil)
		if err != nil {
			t.Fatal(err)
		}
		cases = append(cases, matchCase{value.method, value.url, dnsresolver.IsDoHRequest(r)})
	}
	encoded, _ := json.Marshal(cases)
	program := `import assert from 'node:assert/strict';
const seen=[];
globalThis.fetch=(...args)=>{seen.push(['native',...args]);return Promise.resolve(new Response('native'))};
const env={__NANOFLARE_DNS:{fetch:(...args)=>{seen.push(['dns',...args]);return Promise.resolve(new Response('dns'))}}};
` + strings.Replace(dohFetchSource(), `import { env } from "cloudflare:workers";`, "", 1) + `
const url='https://cloudflare-dns.com/dns-query?name=private.internal&type=A';
for (const input of [url,new URL(url),new Request(url)]) {
 assert.equal(await (await fetch(input)).text(),'dns');
}
assert.equal(await (await fetch(new Request(url,{method:'POST'}))).text(),'native');
const post=new Request(url,{method:'POST',body:'doh-post'});
await fetch(post);
assert.equal(seen.at(-1)[1],post);
assert.equal(post.bodyUsed,false);
assert.equal(await (await fetch(url,{method:'post'})).text(),'native');
assert.equal(await (await fetch(new Request(url,{method:'POST'}),{method:'GET'})).text(),'dns');
assert.equal(await (await fetch('https://cloudflare-dns.com.attacker.test/dns-query?name=x')).text(),'native');
assert.equal(await (await fetch('https://cloudflare-dns.com/other')).text(),'native');
const controller=new AbortController();
await fetch(new Request(url,{headers:{'X-Nanoflare-DNS-Profile':'forged'},signal:controller.signal}));
const [kind,target,init]=seen.at(-1);
assert.equal(kind,'dns');
assert.equal(target,'http://nanoflare.dns/dns-query?name=private.internal&type=A');
assert.equal(new Headers(init.headers).has('X-Nanoflare-DNS-Profile'),false);
controller.abort();
assert.equal(init.signal.aborted,true);
await assert.rejects(fetch(url,{signal:controller.signal}),{name:'AbortError'});
const input=new Request('https://example.com/',{method:'POST',body:'payload'}),options={redirect:'manual'};
await fetch(input,options);
assert.equal(seen.at(-1)[0],'native');
assert.equal(seen.at(-1)[1],input);
assert.equal(seen.at(-1)[2],options);
assert.equal(input.bodyUsed,false);
` + "\nfor (const test of " + string(encoded) + `) {
 await fetch(test.URL,{method:test.Method});
 assert.equal(seen.at(-1)[0]==='dns',test.Intercepted,JSON.stringify(test));
}
`
	if output, err := exec.Command(node, "--input-type=module", "-e", program).CombinedOutput(); err != nil {
		t.Fatalf("fetch hook: %v\n%s", err, output)
	}
}
