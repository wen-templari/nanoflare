package telemetry

import (
	"context"
	"testing"
)

func TestConfigureTracingDisabled(t *testing.T) {
	shutdown, err := ConfigureTracing(context.Background(), TracingConfig{})
	if err != nil {
		t.Fatal(err)
	}
	if err := shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestConfigureTracingRejectsInvalidSampleRatio(t *testing.T) {
	_, err := ConfigureTracing(context.Background(), TracingConfig{
		Endpoint:    "http://127.0.0.1:4317",
		SampleRatio: 1.1,
	})
	if err == nil {
		t.Fatal("expected invalid sample ratio error")
	}
}
