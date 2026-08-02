package database

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/clas/nanoflare/internal/nanoflare"
)

type LitestreamReplicaConfig struct {
	URLPrefix       string
	Endpoint        string
	Region          string
	AccessKeyID     string
	SecretAccessKey string
	ForcePathStyle  bool
}

type LitestreamSupervisor struct {
	enabled        bool
	bin            string
	configPath     string
	generated      bool
	replica        LitestreamReplicaConfig
	databases      map[string]struct{}
	mu             sync.Mutex
	started        bool
	startRequested bool
	ctx            context.Context
	cmd            *exec.Cmd
	socketPath     string
}

func NewLitestreamSupervisor(enabled bool, bin, configPath string) *LitestreamSupervisor {
	if strings.TrimSpace(bin) == "" {
		bin = "litestream"
	}
	return &LitestreamSupervisor{enabled: enabled, bin: bin, configPath: strings.TrimSpace(configPath), databases: make(map[string]struct{})}
}

func (s *LitestreamSupervisor) Enabled() bool {
	return s != nil && s.enabled
}

func (s *LitestreamSupervisor) Start(ctx context.Context) error {
	if !s.Enabled() {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.startRequested = true
	s.ctx = ctx
	if s.generated && len(s.databases) == 0 {
		return s.writeGeneratedConfigLocked()
	}
	return s.startLocked(ctx)
}

func (s *LitestreamSupervisor) UseGeneratedConfig(configPath string, replica LitestreamReplicaConfig) error {
	if !s.Enabled() {
		return nil
	}
	replica.URLPrefix = strings.TrimRight(strings.TrimSpace(replica.URLPrefix), "/")
	if replica.URLPrefix == "" {
		return errors.New("Litestream replica URL prefix is required when generated config is used")
	}
	if strings.TrimSpace(replica.Region) == "" {
		replica.Region = "us-east-1"
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.generated = true
	s.configPath = strings.TrimSpace(configPath)
	s.socketPath = filepath.Join(filepath.Dir(s.configPath), "litestream.sock")
	s.replica = replica
	return s.writeGeneratedConfigLocked()
}

func (s *LitestreamSupervisor) EnsureDatabase(dbPath string) error {
	return s.ensureDatabase(dbPath, true)
}

func (s *LitestreamSupervisor) ConfigureDatabase(dbPath string) error {
	return s.ensureDatabase(dbPath, false)
}

func (s *LitestreamSupervisor) ensureDatabase(dbPath string, start bool) error {
	if !s.Enabled() || !s.generated {
		return nil
	}
	dbPath = strings.TrimSpace(dbPath)
	if dbPath == "" {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.databases[dbPath]; ok {
		if start && s.startRequested && !s.started {
			return s.startLocked(s.ctx)
		}
		return nil
	}
	s.databases[dbPath] = struct{}{}
	if err := s.writeGeneratedConfigLocked(); err != nil {
		return err
	}
	// A generated daemon has a control socket, so register databases without
	// interrupting replication for every other database.
	if start && s.started {
		if err := s.registerLocked(dbPath); err == nil {
			return nil
		}
		// Keep the config authoritative if an older Litestream binary does not
		// support registration yet.
		s.stopLocked()
	}
	if start && s.startRequested {
		return s.startLocked(s.ctx)
	}
	return nil
}

func (s *LitestreamSupervisor) registerLocked(dbPath string) error {
	if s.socketPath == "" {
		return errors.New("Litestream control socket is not configured")
	}
	args := []string{"register", "-socket", s.socketPath, "-replica", s.replica.URLPrefix + "/" + filepath.Base(dbPath), dbPath}
	output, err := exec.Command(s.bin, args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("litestream register: %w: %s", err, strings.TrimSpace(string(output)))
	}
	return nil
}

func (s *LitestreamSupervisor) Status(dbPath string) (nanoflare.DatabaseReplicationStatus, error) {
	status := nanoflare.DatabaseReplicationStatus{Enabled: s.Enabled(), RecoveryHours: 168}
	if !s.Enabled() {
		status.State = "disabled"
		return status, nil
	}
	s.mu.Lock()
	socketPath, configPath := s.socketPath, s.configPath
	started := s.started
	s.mu.Unlock()
	if absolute, err := filepath.Abs(dbPath); err == nil {
		dbPath = absolute
	}
	if !started || socketPath == "" {
		status.State = "unavailable"
		status.Error = "Litestream is not running"
	}
	type item struct {
		Path       string     `json:"path"`
		Status     string     `json:"status"`
		LastSyncAt *time.Time `json:"last_sync_at"`
	}
	var list struct {
		Databases []item `json:"databases"`
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	output, err := exec.CommandContext(ctx, s.bin, "list", "-json", "-socket", socketPath).CombinedOutput()
	if started && socketPath != "" && (err != nil || json.Unmarshal(output, &list) != nil) {
		status.State, status.Error = "error", "Litestream control socket is unavailable"
	} else if started && socketPath != "" {
		status.State = "stopped"
		for _, db := range list.Databases {
			if db.Path == dbPath {
				status.State, status.LastSyncAt = db.Status, db.LastSyncAt
				if db.LastSyncAt != nil {
					status.SyncAgeSeconds = time.Since(*db.LastSyncAt).Seconds()
				}
				break
			}
		}
	}
	var local []struct {
		Path      string          `json:"database"`
		LocalTXID json.RawMessage `json:"local_txid"`
		WALSize   json.RawMessage `json:"wal_size"`
	}
	if output, err = exec.Command(s.bin, "status", "-json", "-config", configPath).CombinedOutput(); err == nil && json.Unmarshal(output, &local) == nil {
		for _, db := range local {
			if db.Path == dbPath {
				status.LocalTXID = parseTXID(db.LocalTXID)
				status.WALBytes = parseBytes(db.WALSize)
				break
			}
		}
	}
	var files []struct {
		Level     int       `json:"level"`
		MaxTXID   string    `json:"max_txid"`
		Timestamp time.Time `json:"timestamp"`
	}
	if output, err = exec.Command(s.bin, "ltx", "-json", "-level", "all", "-config", configPath, dbPath).CombinedOutput(); err == nil && json.Unmarshal(output, &files) == nil {
		for _, file := range files {
			if !file.Timestamp.IsZero() && (status.EarliestRecoveryAt == nil || file.Timestamp.Before(*status.EarliestRecoveryAt)) {
				point := file.Timestamp
				status.EarliestRecoveryAt = &point
			}
			if file.Level == 9 && !file.Timestamp.IsZero() {
				status.RestorePoints = append(status.RestorePoints, nanoflare.DatabaseRestorePoint{Timestamp: file.Timestamp, TXID: file.MaxTXID})
			}
		}
		sort.Slice(status.RestorePoints, func(i, j int) bool { return status.RestorePoints[i].Timestamp.After(status.RestorePoints[j].Timestamp) })
	}
	return status, nil
}

func parseTXID(raw json.RawMessage) uint64 {
	var n uint64
	if json.Unmarshal(raw, &n) == nil {
		return n
	}
	var value string
	_ = json.Unmarshal(raw, &value)
	n, _ = strconv.ParseUint(strings.TrimPrefix(value, "0x"), 16, 64)
	return n
}
func parseBytes(raw json.RawMessage) int64 {
	var n int64
	if json.Unmarshal(raw, &n) == nil {
		return n
	}
	var value string
	_ = json.Unmarshal(raw, &value)
	fields := strings.Fields(value)
	if len(fields) > 0 {
		n, _ = strconv.ParseInt(fields[0], 10, 64)
	}
	return n
}

func (s *LitestreamSupervisor) startLocked(ctx context.Context) error {
	if s.started {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	args := []string{"replicate"}
	if s.configPath != "" {
		args = append(args, "-config", s.configPath)
	}
	cmd := exec.CommandContext(ctx, s.bin, args...)
	if err := cmd.Start(); err != nil {
		return err
	}
	s.started = true
	s.cmd = cmd
	go func() {
		err := cmd.Wait()
		s.mu.Lock()
		if s.cmd == cmd {
			s.started = false
			s.cmd = nil
		}
		s.mu.Unlock()
		if err != nil && !errors.Is(ctx.Err(), context.Canceled) {
			log.Printf("litestream exited: %v", err)
		}
	}()
	return nil
}

func (s *LitestreamSupervisor) Restore(dbPath string) error {
	if !s.Enabled() {
		return nil
	}
	if err := s.ConfigureDatabase(dbPath); err != nil {
		return err
	}
	args := []string{"restore", "-if-db-not-exists", "-if-replica-exists"}
	if s.configPath != "" {
		args = append(args, "-config", s.configPath)
	}
	args = append(args, dbPath)
	output, err := exec.Command(s.bin, args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("litestream restore: %w: %s", err, strings.TrimSpace(string(output)))
	}
	return nil
}

func (s *LitestreamSupervisor) RestoreTo(dbPath, outputPath string, timestamp *time.Time) error {
	if !s.Enabled() {
		return errors.New("Litestream is disabled")
	}
	args := []string{"restore", "-force", "-integrity-check", "quick", "-o", outputPath}
	if timestamp != nil {
		args = append(args, "-timestamp", timestamp.UTC().Format(time.RFC3339))
	}
	if s.configPath != "" {
		args = append(args, "-config", s.configPath)
	}
	args = append(args, dbPath)
	preflight := append([]string{}, args...)
	preflight = append(preflight[:1], append([]string{"-dry-run"}, preflight[1:]...)...)
	if output, err := exec.Command(s.bin, preflight...).CombinedOutput(); err != nil {
		return fmt.Errorf("litestream restore preflight: %w: %s", err, strings.TrimSpace(string(output)))
	}
	output, err := exec.Command(s.bin, args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("litestream restore: %w: %s", err, strings.TrimSpace(string(output)))
	}
	return nil
}

func (s *LitestreamSupervisor) stopLocked() {
	if s.cmd == nil || s.cmd.Process == nil {
		s.started = false
		s.cmd = nil
		return
	}
	if err := s.cmd.Process.Kill(); err != nil {
		log.Printf("stop litestream: %v", err)
	}
	s.started = false
	s.cmd = nil
}

func (s *LitestreamSupervisor) writeGeneratedConfigLocked() error {
	if !s.generated {
		return nil
	}
	if s.configPath == "" {
		return errors.New("generated Litestream config path is required")
	}
	if err := os.MkdirAll(filepath.Dir(s.configPath), 0o700); err != nil {
		return err
	}
	paths := make([]string, 0, len(s.databases))
	for path := range s.databases {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	var out strings.Builder
	out.WriteString("# Generated by nanoflared. Do not edit by hand.\n")
	out.WriteString("socket:\n  enabled: true\n  path: ")
	out.WriteString(strconv.Quote(s.socketPath))
	out.WriteString("\n  permissions: 0600\nsnapshot:\n  interval: 1h\n  retention: 168h\n")
	// These top-level replica defaults are also used by databases registered
	// through the daemon socket after the process has started.
	if s.replica.Endpoint != "" {
		out.WriteString("endpoint: ")
		out.WriteString(strconv.Quote(s.replica.Endpoint))
		out.WriteString("\n")
	}
	if s.replica.Region != "" {
		out.WriteString("region: ")
		out.WriteString(strconv.Quote(s.replica.Region))
		out.WriteString("\n")
	}
	if s.replica.AccessKeyID != "" {
		out.WriteString("access-key-id: ")
		out.WriteString(strconv.Quote(s.replica.AccessKeyID))
		out.WriteString("\n")
	}
	if s.replica.SecretAccessKey != "" {
		out.WriteString("secret-access-key: ")
		out.WriteString(strconv.Quote(s.replica.SecretAccessKey))
		out.WriteString("\n")
	}
	if s.replica.ForcePathStyle {
		out.WriteString("force-path-style: true\n")
	}
	if len(paths) == 0 {
		out.WriteString("dbs: []\n")
		return os.WriteFile(s.configPath, []byte(out.String()), 0o600)
	}
	out.WriteString("dbs:\n")
	for _, path := range paths {
		out.WriteString("  - path: ")
		out.WriteString(strconv.Quote(path))
		out.WriteString("\n")
		out.WriteString("    replica:\n")
		out.WriteString("      url: ")
		out.WriteString(strconv.Quote(s.replica.URLPrefix + "/" + filepath.Base(path)))
		out.WriteString("\n")
		if s.replica.Endpoint != "" {
			out.WriteString("      endpoint: ")
			out.WriteString(strconv.Quote(s.replica.Endpoint))
			out.WriteString("\n")
		}
		if s.replica.Region != "" {
			out.WriteString("      region: ")
			out.WriteString(strconv.Quote(s.replica.Region))
			out.WriteString("\n")
		}
		if s.replica.AccessKeyID != "" {
			out.WriteString("      access-key-id: ")
			out.WriteString(strconv.Quote(s.replica.AccessKeyID))
			out.WriteString("\n")
		}
		if s.replica.SecretAccessKey != "" {
			out.WriteString("      secret-access-key: ")
			out.WriteString(strconv.Quote(s.replica.SecretAccessKey))
			out.WriteString("\n")
		}
		if s.replica.ForcePathStyle {
			out.WriteString("      force-path-style: true\n")
		}
	}
	return os.WriteFile(s.configPath, []byte(out.String()), 0o600)
}
