package nanoflare

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"regexp"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"
)

type TelemetryPolicy struct {
	Enabled        bool     `json:"enabled"`
	SamplePercent  int      `json:"sample_percent"`
	RedactPatterns []string `json:"redact_patterns,omitempty"`
}
type telemetryWindow struct {
	minute                 int64
	lines, bytes, sequence int
}
type telemetryState struct {
	mu       sync.Mutex
	policy   TelemetryPolicy
	patterns []*regexp.Regexp
	workers  map[string]telemetryWindow
	dropped  atomic.Uint64
}

func (s *Service) SetTelemetryPolicy(policy TelemetryPolicy) error {
	if policy.SamplePercent < 0 || policy.SamplePercent > 100 || len(policy.RedactPatterns) > 16 {
		return errors.New("invalid telemetry policy")
	}
	patterns := []*regexp.Regexp{}
	for _, pattern := range policy.RedactPatterns {
		if len(pattern) > 512 {
			return errors.New("redaction pattern too long")
		}
		compiled, err := regexp.Compile(pattern)
		if err != nil {
			return err
		}
		patterns = append(patterns, compiled)
	}
	policy.RedactPatterns = append([]string{}, policy.RedactPatterns...)
	s.telemetry.mu.Lock()
	defer s.telemetry.mu.Unlock()
	s.telemetry.policy = policy
	s.telemetry.patterns = patterns
	return nil
}
func (s *Service) RuntimeTelemetryDropped() uint64 { return s.telemetry.dropped.Load() }
func correlationID(value string) string {
	if value == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:16])
}

var credentialLogPattern = regexp.MustCompile(`\b(?:nfc1\.[A-Za-z0-9_.-]+|nfi1\.[A-Za-z0-9_-]+|nfexec_[A-Za-z0-9]+|nfgrant_[A-Za-z0-9]+)\b`)

func boundedLogMessage(message string) string {
	if len(message) > 4096 {
		message = message[:4096]
		for !utf8.ValidString(message) {
			message = message[:len(message)-1]
		}
	}
	return strings.ToValidUTF8(message, "�")
}

// ProcessRuntimeLog binds producer metadata to authorized runtime identity and
// applies collection policy before any message reaches buffers or forwarding.
func (s *Service) ProcessRuntimeLog(ctx context.Context, token, level, message string) (WorkerOutputLine, bool, error) {
	principal, err := s.AuthorizeRuntime(ctx, token, RuntimeLogs, "")
	if err != nil {
		return WorkerOutputLine{}, false, err
	}
	if len(message) > 4096 {
		s.telemetry.dropped.Add(1)
		return WorkerOutputLine{}, false, nil
	}
	now := time.Now().UTC()
	minute := now.Unix() / 60
	s.telemetry.mu.Lock()
	policy := s.telemetry.policy
	if s.telemetry.workers == nil {
		s.telemetry.workers = map[string]telemetryWindow{}
	}
	window := s.telemetry.workers[principal.App.ID]
	if window.minute != minute {
		window = telemetryWindow{minute: minute}
	}
	if len(s.telemetry.workers) >= 4096 && window.lines == 0 {
		for id, old := range s.telemetry.workers {
			if old.minute != minute {
				delete(s.telemetry.workers, id)
			}
		}
	}
	if !policy.Enabled || policy.SamplePercent == 0 || window.lines >= 100 || window.bytes+len(message) > 256<<10 || len(s.telemetry.workers) >= 4096 && window.lines == 0 {
		s.telemetry.mu.Unlock()
		s.telemetry.dropped.Add(1)
		return WorkerOutputLine{}, false, nil
	}
	window.lines++
	window.bytes += len(message)
	window.sequence++
	s.telemetry.workers[principal.App.ID] = window
	patterns := append([]*regexp.Regexp{}, s.telemetry.patterns...)
	sampled := (window.sequence-1)%100 < policy.SamplePercent
	s.telemetry.mu.Unlock()
	if !sampled {
		s.telemetry.dropped.Add(1)
		return WorkerOutputLine{}, false, nil
	}
	line := WorkerOutputLine{Timestamp: now, AppID: principal.App.ID, DeploymentID: principal.Deployment.ID, OrgID: principal.App.OrgID, Level: level}
	switch line.Level {
	case "debug", "info", "warn", "error":
	default:
		line.Level = "info"
	}
	if principal.App.Platform != nil {
		line.NamespaceID = principal.App.Platform.NamespaceID
		line.CustomerID = principal.App.Platform.CustomerID
	}
	var secrets map[string]string
	if strings.HasPrefix(token, invocationCapabilityPrefix) {
		s.dispatch.mu.Lock()
		lease := s.dispatch.leases[token]
		if lease == nil || lease.LogLines >= 64 {
			s.dispatch.mu.Unlock()
			s.telemetry.dropped.Add(1)
			return WorkerOutputLine{}, false, nil
		}
		lease.LogLines++
		secrets = lease.Target.App.SecretValues
		chain := lease.Root
		if chain == "" {
			chain = token
		}
		line.RequestChainID = correlationID(chain)
		line.InvocationID = correlationID(token)
		line.ParentInvocationID = correlationID(lease.Parent)
		s.dispatch.mu.Unlock()
	} else {
		active := ActiveDeployment{App: principal.App, Deployment: principal.Deployment}
		if err = s.hydrateArtifact(&active); err != nil {
			return WorkerOutputLine{}, false, err
		}
		secrets = active.App.SecretValues
	}

	redactions := []string{}
	for _, secret := range secrets {
		if secret == "" {
			continue
		}
		redactions = append(redactions, secret)
		encoded, _ := json.Marshal(secret)
		if len(encoded) > 2 {
			redactions = append(redactions, string(encoded[1:len(encoded)-1]))
		}
	}
	sort.Slice(redactions, func(i, j int) bool { return len(redactions[i]) > len(redactions[j]) })
	for _, secret := range redactions {
		message = strings.ReplaceAll(message, secret, "[redacted]")
	}
	message = credentialLogPattern.ReplaceAllString(message, "[redacted]")
	for _, pattern := range patterns {
		message = pattern.ReplaceAllString(message, "[redacted]")
	}
	line.Message = boundedLogMessage(message)
	return line, true, nil
}
