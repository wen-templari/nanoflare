package config

import (
	"github.com/clas/nanoflare/internal/nanoflare"
	"testing"
)

func TestRuntimeOptionsDetachesCompilerConfiguration(t *testing.T) {
	writers := map[string]interface {
		SetNanoflareRuntimeAddr(string)
		SetNetworkAllow([]string)
		SetWorkerdEgressAddr(string)
		SetWorkerdDNSAddr(string)
		RuntimeOptions() WorkerdOptions
	}{"writer": NewWriter("", "", "", ""), "runtime": NewRuntimeWriter("", nil)}
	for name, writer := range writers {
		t.Run(name, func(t *testing.T) {
			writer.SetNanoflareRuntimeAddr("127.0.0.1:8001")
			writer.SetNetworkAllow([]string{"public"})
			writer.SetWorkerdEgressAddr("127.0.0.1:8002")
			writer.SetWorkerdDNSAddr("127.0.0.1:8003")
			options := writer.RuntimeOptions()
			if options.RuntimeAddr != "127.0.0.1:8001" || options.EgressAddr != "127.0.0.1:8002" || options.DNSAddr != "127.0.0.1:8003" {
				t.Fatalf("options = %#v", options)
			}
			options.NetworkAllow[0] = "modified"
			if writer.RuntimeOptions().NetworkAllow[0] != "public" {
				t.Fatal("options alias writer configuration")
			}
		})
	}
}

func TestValidateRuntimePreservesDNSProfileValidation(t *testing.T) {
	writers := []interface {
		SetDNSProfiles([]string)
		ValidateRuntime([]nanoflare.ActiveDeployment) error
	}{NewWriter("", "", "", ""), NewRuntimeWriter("", nil)}
	for _, writer := range writers {
		writer.SetDNSProfiles([]string{"default"})
		active := []nanoflare.ActiveDeployment{{}}
		active[0].Deployment.DNS.Profile = "unknown"
		if err := writer.ValidateRuntime(active); err == nil {
			t.Fatal("unknown DNS profile accepted")
		}
		active[0].Deployment.DNS.Profile = "default"
		if err := writer.ValidateRuntime(active); err != nil {
			t.Fatal(err)
		}
	}
}
