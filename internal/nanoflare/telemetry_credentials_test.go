package nanoflare

import "testing"

func TestRuntimeCredentialsAreRedactedFromLogs(t *testing.T) {
	for _, token := range []string{"nfc1.payload.signature", "nfi1.invocation", "nfexec_abcd1234", "nfgrant_abcd1234"} {
		if got := credentialLogPattern.ReplaceAllString("token="+token, "[redacted]"); got != "token=[redacted]" {
			t.Fatalf("runtime credential leaked: %q", got)
		}
	}
}
