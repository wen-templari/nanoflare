package runtime

import (
	"bytes"
	"strings"
	"sync"
	"time"

	"github.com/clas/nanoflare/internal/nanoflare"
)

const maxOutputLines = 4000
const maxWorkerOutputLines = 200

// OutputBuffer captures the shared workerd process stream for the console.
// A workerd generation hosts multiple isolates, so its raw output is shared.
type OutputBuffer struct {
	mu      sync.RWMutex
	pending []byte
	lines   []*nanoflare.WorkerOutputLine
	forward *VectorForwarder
}

func NewOutputBuffer() *OutputBuffer {
	return &OutputBuffer{}
}

func (b *OutputBuffer) SetForwarder(forwarder *VectorForwarder) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.forward = forwarder
}

func (b *OutputBuffer) Write(value []byte) (int, error) {
	originalLength := len(value)
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(value) > 64<<10 {
		value = value[len(value)-(64<<10):]
	}
	b.pending = append(b.pending, value...)
	if len(b.pending) > 64<<10 {
		b.pending = b.pending[len(b.pending)-(64<<10):]
	}
	for {
		index := bytes.IndexByte(b.pending, '\n')
		if index < 0 {
			break
		}
		b.append(strings.TrimSpace(string(b.pending[:index])))
		b.pending = b.pending[index+1:]
	}
	return originalLength, nil
}

func (b *OutputBuffer) Append(level, message string) {
	b.AppendScoped("", "", level, message)
}

func (b *OutputBuffer) AppendActive(active nanoflare.ActiveDeployment, level, message string) {
	b.AppendScoped(active.App.ID, active.Deployment.ID, level, message)
}

func (b *OutputBuffer) AppendScoped(appID, deploymentID, level, message string) {
	b.AppendLine(nanoflare.WorkerOutputLine{Timestamp: time.Now().UTC(), Level: level, Message: message, AppID: appID, DeploymentID: deploymentID})
}
func (b *OutputBuffer) AppendLine(line nanoflare.WorkerOutputLine) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.lines = append(b.lines, &line)
	count := 0
	for i := len(b.lines) - 1; i >= 0; i-- {
		if b.lines[i].AppID == line.AppID {
			count++
			if count > maxWorkerOutputLines {
				b.lines = append(b.lines[:i], b.lines[i+1:]...)
			}
		}
	}
	if b.forward != nil {
		b.forward.Append(line)
	}
	b.trim()
}

func (b *OutputBuffer) Output(appID string) []nanoflare.WorkerOutputLine {
	b.mu.RLock()
	defer b.mu.RUnlock()
	result := make([]nanoflare.WorkerOutputLine, 0, len(b.lines))
	for _, line := range b.lines {
		if line.AppID == appID {
			result = append(result, *line)
		}
	}
	return result
}

// Shared stdout/stderr is untrusted text. Only authenticated native tail events
// may call AppendScoped; a marker or continuation can never assign producer identity.
func (b *OutputBuffer) append(message string) {
	if len(message) > 4096 {
		message = "[runtime diagnostic omitted: exceeds collection limit]"
	}
	if message == "" {
		return
	}
	b.lines = append(b.lines, &nanoflare.WorkerOutputLine{Timestamp: time.Now().UTC(), Level: "info", Message: message})
	b.trim()
}

func (b *OutputBuffer) trim() {
	if len(b.lines) > maxOutputLines {
		b.lines = append([]*nanoflare.WorkerOutputLine(nil), b.lines[len(b.lines)-maxOutputLines:]...)
	}
}
