package config

import "testing"

func TestEnvBoolOrDefault(t *testing.T) {
	for _, test := range []struct {
		value          string
		fallback, want bool
	}{{"", true, true}, {" ", false, false}, {" true ", false, true}, {"false", true, false}, {"1", false, true}, {"0", true, false}} {
		t.Setenv("NANOFLARE_TEST_BOOL", test.value)
		if got := EnvBoolOrDefault("NANOFLARE_TEST_BOOL", test.fallback); got != test.want {
			t.Fatalf("%q got %v", test.value, got)
		}
	}
}
