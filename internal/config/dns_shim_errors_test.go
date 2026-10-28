package config

import (
	"os/exec"
	"testing"
)

func TestDNSShimPreservesBrokerAndResolverErrors(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed")
	}
	script := `import assert from 'node:assert/strict';
` + dnsShimWithBinding("", "globalThis.transport") + `
for (const [status,body,code,message] of [
 [410,{detail:'expired lease'},'ERR_NANOFLARE_INVOCATION_EXPIRED','expired lease'],
 [429,{detail:'budget exhausted'},'ERR_NANOFLARE_EFFECT_LIMIT','budget exhausted'],
 [403,{detail:'denied'},'ERR_NANOFLARE_ACCESS_DENIED','denied'],
 [410,{code:'ERR_NANOFLARE_INVOCATION_EXPIRED',error:'expired'},'ERR_NANOFLARE_INVOCATION_EXPIRED','expired'],
 [502,{code:'ENOTFOUND',error:'no such host'},'ENOTFOUND','no such host'],
 [503,{code:'EAI_AGAIN',error:'unavailable'},'EAI_AGAIN','unavailable'],
 [404,'old daemon: not found','ERR_NANOFLARE_DNS_UNAVAILABLE','old daemon: not found'],
 [405,'method not allowed','ERR_NANOFLARE_DNS_UNAVAILABLE','method not allowed'],
 [200,'broken response','ERR_NANOFLARE_DNS_RESPONSE','broken response'],
]) {
 globalThis.transport={fetch:async()=>typeof body === "string" ? new Response(body,{status}) : Response.json(body,{status})};
 const check=e=>e.code===code && e.status===status && e.message===message && e.hostname==='db.invalid' && e.syscall==='getaddrinfo';
 await assert.rejects(promises.lookup('db.invalid'),check);
 await new Promise((resolve,reject)=>lookup('db.invalid', e=>{try{assert.ok(check(e));resolve()}catch(e){reject(e)}}));
}
`
	if output, err := exec.Command(node, "--input-type=module", "-e", script).CombinedOutput(); err != nil {
		t.Fatalf("shim errors: %v\n%s", err, output)
	}
}
