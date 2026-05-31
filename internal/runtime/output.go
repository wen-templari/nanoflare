package runtime

import (
	"bytes"
	"strings"
	"sync"
	"time"

	"github.com/clas/platform/internal/platform"
)

const maxOutputLines = 200

// OutputBuffer captures the shared workerd process stream for the console.
// A workerd generation hosts multiple isolates, so its raw output is shared.
type OutputBuffer struct {
	mu      sync.RWMutex
	pending []byte
	lines   []platform.WorkerOutputLine
}

func NewOutputBuffer() *OutputBuffer {
	return &OutputBuffer{}
}

func (b *OutputBuffer) Write(value []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.pending = append(b.pending, value...)
	for {
		index := bytes.IndexByte(b.pending, '\n')
		if index < 0 {
			break
		}
		b.append(strings.TrimSpace(string(b.pending[:index])))
		b.pending = b.pending[index+1:]
	}
	return len(value), nil
}

func (b *OutputBuffer) Append(level, message string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.lines = append(b.lines, platform.WorkerOutputLine{
		Timestamp: time.Now().UTC(),
		Level:     level,
		Message:   message,
	})
	b.trim()
}

func (b *OutputBuffer) Output(_ string) []platform.WorkerOutputLine {
	b.mu.RLock()
	defer b.mu.RUnlock()
	result := make([]platform.WorkerOutputLine, len(b.lines))
	copy(result, b.lines)
	return result
}

func (b *OutputBuffer) append(message string) {
	if message == "" {
		return
	}
	level := "info"
	lower := strings.ToLower(message)
	if strings.Contains(lower, "error") || strings.Contains(lower, "fatal") {
		level = "error"
	} else if strings.Contains(lower, "warn") {
		level = "warn"
	}
	b.lines = append(b.lines, platform.WorkerOutputLine{
		Timestamp: time.Now().UTC(),
		Level:     level,
		Message:   message,
	})
	b.trim()
}

func (b *OutputBuffer) trim() {
	if len(b.lines) > maxOutputLines {
		b.lines = append([]platform.WorkerOutputLine(nil), b.lines[len(b.lines)-maxOutputLines:]...)
	}
}
