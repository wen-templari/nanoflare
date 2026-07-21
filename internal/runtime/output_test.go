package runtime

import "testing"

func TestOutputBufferCapturesAndClassifiesLines(t *testing.T) {
	buffer := NewOutputBuffer()
	if _, err := buffer.Write([]byte("ready\nwarning: slow request\npartial")); err != nil {
		t.Fatal(err)
	}
	if _, err := buffer.Write([]byte(" line\nfatal error\n")); err != nil {
		t.Fatal(err)
	}
	lines := buffer.Output("")
	if len(lines) != 4 {
		t.Fatalf("got %d lines, want 4", len(lines))
	}
	if lines[0].Level != "info" || lines[1].Level != "warn" || lines[2].Message != "partial line" || lines[3].Level != "error" {
		t.Fatalf("unexpected output: %#v", lines)
	}
}

func TestOutputBufferFiltersScopedLinesByWorker(t *testing.T) {
	buffer := NewOutputBuffer()
	buffer.AppendScoped("app-one", "dep-one", "info", "first")
	buffer.AppendScoped("app-two", "dep-two", "info", "second")
	buffer.Append("info", "shared")

	lines := buffer.Output("app-one")
	if len(lines) != 1 {
		t.Fatalf("got %d lines, want 1: %#v", len(lines), lines)
	}
	if lines[0].Message != "first" || lines[0].AppID != "app-one" || lines[0].DeploymentID != "dep-one" {
		t.Fatalf("unexpected scoped line: %#v", lines[0])
	}
}

func TestOutputBufferParsesAndStripsIdentityPrefix(t *testing.T) {
	buffer := NewOutputBuffer()
	if _, err := buffer.Write([]byte("[[nanoflare-output app=app-one deployment=dep-one]] user ready\n")); err != nil {
		t.Fatal(err)
	}

	lines := buffer.Output("app-one")
	if len(lines) != 1 {
		t.Fatalf("got %d lines, want 1: %#v", len(lines), lines)
	}
	if lines[0].Message != "user ready" || lines[0].AppID != "app-one" || lines[0].DeploymentID != "dep-one" {
		t.Fatalf("unexpected parsed line: %#v", lines[0])
	}
}

func TestOutputBufferStripsRepeatedIdentityPrefixes(t *testing.T) {
	buffer := NewOutputBuffer()
	input := "[[nanoflare-output app=app-one deployment=dep-one]] [[nanoflare-output app=app-one deployment=dep-one]] [gallery serve] object metadata {\n"
	if _, err := buffer.Write([]byte(input)); err != nil {
		t.Fatal(err)
	}

	lines := buffer.Output("app-one")
	if len(lines) != 1 {
		t.Fatalf("got %d lines, want 1: %#v", len(lines), lines)
	}
	if lines[0].Message != "[gallery serve] object metadata {" {
		t.Fatalf("message = %q", lines[0].Message)
	}
}

func TestOutputBufferAppendsMultilineConsoleContinuation(t *testing.T) {
	buffer := NewOutputBuffer()
	input := "[[nanoflare-output app=app-one deployment=dep-one]] [gallery serve] object metadata {\n  id: '5c8632',\n  size: 204082\n}\n"
	if _, err := buffer.Write([]byte(input)); err != nil {
		t.Fatal(err)
	}

	lines := buffer.Output("app-one")
	if len(lines) != 1 {
		t.Fatalf("got %d lines, want 1: %#v", len(lines), lines)
	}
	want := "[gallery serve] object metadata {\nid: '5c8632',\nsize: 204082\n}"
	if lines[0].Message != want {
		t.Fatalf("message = %q, want %q", lines[0].Message, want)
	}
}
