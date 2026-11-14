package nanoflare

import "testing"

func TestOutboundExecutionSnapshotIsDetached(t *testing.T) {
	active := ActiveDeployment{App: App{ID: "host"}, Deployment: Deployment{ID: "revision", AppID: "host", DispatchNamespaces: []DispatchBinding{{Binding: "D", NamespaceID: "ns", Outbound: &DispatchOutbound{WorkerID: "policy", Parameters: []string{"context"}}}}}}
	frozen, err := FreezeExecution(active)
	if err != nil {
		t.Fatal(err)
	}
	active.Deployment.DispatchNamespaces[0].Outbound.Parameters[0] = "changed"
	active.Deployment.DispatchNamespaces[0].Outbound.WorkerID = "different-policy"
	copy := frozen.ActiveDeployment()
	if copy.Deployment.DispatchNamespaces[0].Outbound.WorkerID != "policy" || copy.Deployment.DispatchNamespaces[0].Outbound.Parameters[0] != "context" {
		t.Fatal("frozen outbound configuration aliases its input")
	}
	copy.Deployment.DispatchNamespaces[0].Outbound.Parameters[0] = "changed-again"
	if frozen.ActiveDeployment().Deployment.DispatchNamespaces[0].Outbound.Parameters[0] != "context" {
		t.Fatal("snapshot accessor exposes mutable outbound configuration")
	}
	other, err := FreezeExecution(active)
	if err != nil {
		t.Fatal(err)
	}
	if other.ID() == frozen.ID() {
		t.Fatal("outbound target/parameters do not participate in execution identity")
	}
}

// Selector changes must invalidate immutable code/config identity even when the
// target revision and parameter declaration remain identical.
func TestOutboundEntrypointExecutionIdentity(t *testing.T) {
	active := ActiveDeployment{App: App{ID: "host"}, Deployment: Deployment{ID: "revision", AppID: "host", DispatchNamespaces: []DispatchBinding{{Binding: "D", NamespaceID: "ns", Outbound: &DispatchOutbound{WorkerID: "policy", Entrypoint: "PolicyA"}}}}}
	first, err := FreezeExecution(active)
	if err != nil {
		t.Fatal(err)
	}
	active.Deployment.DispatchNamespaces[0].Outbound.Entrypoint = "PolicyB"
	second, err := FreezeExecution(active)
	if err != nil {
		t.Fatal(err)
	}
	if first.ID() == second.ID() {
		t.Fatal("entrypoint selector missing from execution identity")
	}
	if first.ActiveDeployment().Deployment.DispatchNamespaces[0].Outbound.Entrypoint != "PolicyA" {
		t.Fatal("entrypoint selector aliases mutable input")
	}
}
