package runtime

import "testing"

func TestCompatibilityDateFromVersion(t *testing.T) {
	date, err := compatibilityDateFromVersion("workerd 2026-07-06\n")
	if err != nil {
		t.Fatal(err)
	}
	if date != "2026-07-06" {
		t.Fatalf("compatibility date = %q, want 2026-07-06", date)
	}
}

func TestCompatibilityDateFromVersionRejectsMissingDate(t *testing.T) {
	if _, err := compatibilityDateFromVersion("workerd development build"); err == nil {
		t.Fatal("compatibilityDateFromVersion() error = nil, want missing date error")
	}
}
