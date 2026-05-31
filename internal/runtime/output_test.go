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
	lines := buffer.Output("app-one")
	if len(lines) != 4 {
		t.Fatalf("got %d lines, want 4", len(lines))
	}
	if lines[0].Level != "info" || lines[1].Level != "warn" || lines[2].Message != "partial line" || lines[3].Level != "error" {
		t.Fatalf("unexpected output: %#v", lines)
	}
}
