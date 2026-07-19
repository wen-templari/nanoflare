package database

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os/exec"
	"strings"
	"sync"
)

type LitestreamSupervisor struct {
	enabled    bool
	bin        string
	configPath string
	mu         sync.Mutex
	started    bool
	cmd        *exec.Cmd
}

func NewLitestreamSupervisor(enabled bool, bin, configPath string) *LitestreamSupervisor {
	if strings.TrimSpace(bin) == "" {
		bin = "litestream"
	}
	return &LitestreamSupervisor{enabled: enabled, bin: bin, configPath: strings.TrimSpace(configPath)}
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
	if s.started {
		return nil
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
		s.started = false
		s.cmd = nil
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
	args := []string{"restore", "-if-db-not-exists"}
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
