package runtime

import (
	"bytes"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/clas/nanoflare/internal/nanoflare"
)

const maxOutputLines = 200
const outputIdentityPrefix = "[[nanoflare-output "
const outputForwardDebounce = 250 * time.Millisecond

// OutputBuffer captures the shared workerd process stream for the console.
// A workerd generation hosts multiple isolates, so its raw output is shared.
type OutputBuffer struct {
	mu            sync.RWMutex
	pending       []byte
	lines         []*nanoflare.WorkerOutputLine
	forward       *VectorForwarder
	forwardTimers map[*nanoflare.WorkerOutputLine]*time.Timer
}

func NewOutputBuffer() *OutputBuffer {
	return &OutputBuffer{forwardTimers: make(map[*nanoflare.WorkerOutputLine]*time.Timer)}
}

func (b *OutputBuffer) SetForwarder(forwarder *VectorForwarder) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.forward = forwarder
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
	b.AppendScoped("", "", level, message)
}

func (b *OutputBuffer) AppendActive(active nanoflare.ActiveDeployment, level, message string) {
	b.AppendScoped(active.App.ID, active.Deployment.ID, level, message)
}

func (b *OutputBuffer) AppendScoped(appID, deploymentID, level, message string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	line := nanoflare.WorkerOutputLine{
		Timestamp:    time.Now().UTC(),
		Level:        level,
		Message:      message,
		AppID:        appID,
		DeploymentID: deploymentID,
	}
	b.lines = append(b.lines, &line)
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

func (b *OutputBuffer) append(message string) {
	if message == "" {
		return
	}
	appID, deploymentID, message := parseOutputIdentity(message)
	if appID == "" && b.appendContinuation(message) {
		return
	}
	line := nanoflare.WorkerOutputLine{
		Timestamp:    time.Now().UTC(),
		Level:        "info",
		Message:      message,
		AppID:        appID,
		DeploymentID: deploymentID,
	}
	b.lines = append(b.lines, &line)
	b.scheduleForward(b.lines[len(b.lines)-1])
	b.trim()
}

func (b *OutputBuffer) appendContinuation(message string) bool {
	if len(b.lines) == 0 {
		return false
	}
	last := b.lines[len(b.lines)-1]
	if last.AppID == "" {
		return false
	}
	last.Message += "\n" + message
	b.scheduleForward(last)
	return true
}

// workerd writes multiline console values as separate stdout lines, sometimes
// with a short pause between writes. Forward only after the record has been
// quiet for a moment so Vector receives one assembled record. The pointers in
// b.lines are stable across slice growth, allowing every continuation to reset
// the same timer. Terminal output remains synchronous through MultiWriter.
func (b *OutputBuffer) scheduleForward(line *nanoflare.WorkerOutputLine) {
	if b.forward == nil || line.AppID == "" {
		return
	}
	if timer := b.forwardTimers[line]; timer != nil {
		timer.Stop()
	}
	var timer *time.Timer
	timer = time.AfterFunc(outputForwardDebounce, func() {
		b.mu.Lock()
		if b.forwardTimers[line] != timer {
			b.mu.Unlock()
			return
		}
		delete(b.forwardTimers, line)
		copy := *line
		forwarder := b.forward
		b.mu.Unlock()
		if forwarder != nil {
			forwarder.Append(copy)
		}
	})
	b.forwardTimers[line] = timer
}

func (b *OutputBuffer) trim() {
	if len(b.lines) > maxOutputLines {
		b.lines = append([]*nanoflare.WorkerOutputLine(nil), b.lines[len(b.lines)-maxOutputLines:]...)
	}
}

func parseOutputIdentity(message string) (string, string, string) {
	var appID, deploymentID string
	rest := strings.TrimSpace(message)
	for strings.HasPrefix(rest, outputIdentityPrefix) {
		end := strings.Index(rest, "]]")
		if end < 0 {
			return "", "", message
		}
		fields := strings.Fields(rest[len(outputIdentityPrefix):end])
		for _, field := range fields {
			key, value, ok := strings.Cut(field, "=")
			if !ok {
				continue
			}
			decoded, err := url.QueryUnescape(value)
			if err != nil {
				decoded = value
			}
			switch key {
			case "app":
				appID = decoded
			case "deployment":
				deploymentID = decoded
			}
		}
		rest = strings.TrimSpace(rest[end+2:])
	}
	if appID == "" {
		return "", "", message
	}
	return appID, deploymentID, rest
}
