package config

import (
	"os/exec"
	"testing"

	"github.com/clas/nanoflare/internal/runtimeassets"
)

func TestOutboundParameterSnapshot(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed")
	}
	script := runtimeassets.DispatchOptionsModule + `
import assert from 'node:assert/strict';
const input={outbound:{context:{customer:'alpha',values:[true,null,42]}}};
const snapshot=snapshotDispatchOptions(input);input.outbound.context.customer='changed';
assert.equal(snapshot.outbound.context.customer,'alpha');
assert.deepEqual(snapshotDispatchOptions(),{});
const cycle={};cycle.self=cycle;
for(const value of [null,[],{cpuMs:1},{outbound:null},{outbound:[]},
 {outbound:{value:undefined}},{outbound:{value:()=>{}}},{outbound:{value:NaN}},
 {outbound:{value:Infinity}},{outbound:{value:1n}},{outbound:{value:new Date()}},
 {outbound:{value:cycle}},{outbound:{value:Array(2)}},{outbound:{value:'x'.repeat(16384)}}])
 assert.throws(()=>snapshotDispatchOptions(value),error=>error instanceof TypeError&&error.status===422);
`
	if output, err := exec.Command(node, "--input-type=module", "-e", script).CombinedOutput(); err != nil {
		t.Fatalf("parameter snapshots: %v\n%s", err, output)
	}
}
