package runtime

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/clas/nanoflare/internal/nanoflare"
)

const (
	runtimeHealthCheckInitialInterval = 2 * time.Millisecond
	runtimeHealthCheckMaxInterval     = 2 * time.Millisecond
)

type ConfigWriter interface {
	WriteWorkerd(string, []nanoflare.ActiveDeployment) error
	WriteTraefik([]nanoflare.ActiveDeployment) error
}

type Process interface {
	Done() <-chan error
	Stop(context.Context) error
}

type Launcher interface {
	Launch(string, []nanoflare.ActiveDeployment) (Process, error)
}

type Manager struct {
	mu              sync.Mutex
	writer          ConfigWriter
	launcher        Launcher
	output          *OutputBuffer
	configDir       string
	canonicalConfig string
	healthTimeout   time.Duration
	stopTimeout     time.Duration
	restartDelay    time.Duration
	autoRestart     bool
	generation      int
	active          *pool
	pending         *pool
	closed          bool
	socketDir       string
	retiring        sync.WaitGroup
	revision        uint64
}

type pool struct {
	socketDir  string
	refs       int
	drained    chan struct{}
	configPath string
	process    Process
	active     []nanoflare.ActiveDeployment
	scheduler  *cronRunner
}

func (m *Manager) Ensure(ctx context.Context, active nanoflare.ActiveDeployment) (EnsuredWorker, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return EnsuredWorker{}, err
	}
	p := m.active
	if p == nil || m.closed {
		return EnsuredWorker{}, errors.New("runtime unavailable")
	}
	for _, item := range p.active {
		if item.App.ID != active.App.ID || item.Deployment.ID != active.Deployment.ID {
			continue
		}
		if p.refs == 0 {
			p.drained = make(chan struct{})
		}
		p.refs++
		var once sync.Once
		return EnsuredWorker{SocketPath: item.SocketPath, Release: func() {
			once.Do(func() {
				m.mu.Lock()
				defer m.mu.Unlock()
				p.refs--
				if p.refs == 0 {
					close(p.drained)
				}
			})
		}}, nil
	}
	return EnsuredWorker{}, errors.New("deployment is not loaded")
}

func NewManager(writer ConfigWriter, launcher Launcher, configDir, canonicalConfig, socketDir string, healthTimeout, stopTimeout time.Duration) *Manager {
	var output *OutputBuffer
	switch value := launcher.(type) {
	case CommandLauncher:
		output = value.Output
	case *CommandLauncher:
		output = value.Output
	}
	return &Manager{
		writer:          writer,
		launcher:        launcher,
		output:          output,
		configDir:       configDir,
		canonicalConfig: canonicalConfig,
		socketDir:       socketDir,
		healthTimeout:   healthTimeout,
		stopTimeout:     stopTimeout,
		restartDelay:    time.Second,
		autoRestart:     true,
	}
}

func (m *Manager) SetAutoRestart(enabled bool) {
	m.autoRestart = enabled
}

func (m *Manager) Write(active []nanoflare.ActiveDeployment) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.write(active)
}

func (m *Manager) write(active []nanoflare.ActiveDeployment) error {
	generation, routed, err := m.prepare(active)
	if err != nil {
		return err
	}
	if err := m.writer.WriteTraefik(routed); err != nil {
		m.abort(generation)
		return err
	}
	return m.commit(generation)
}

func (m *Manager) Prepare(active []nanoflare.ActiveDeployment) (string, []nanoflare.ActiveDeployment, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.prepare(active)
}

func (m *Manager) Commit(generation string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.commit(generation)
}

func (m *Manager) Abort(generation string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.abort(generation)
}

func (m *Manager) prepare(active []nanoflare.ActiveDeployment) (string, []nanoflare.ActiveDeployment, error) {
	if m.closed {
		return "", nil, errors.New("runtime manager is closed")
	}
	if m.pending != nil {
		return "", nil, errors.New("runtime generation is already pending")
	}

	if len(active) == 0 {
		m.pending = &pool{active: []nanoflare.ActiveDeployment{}}
		return "empty", []nanoflare.ActiveDeployment{}, nil
	}

	generation, socketDir, err := m.runtimeEndpoints(active)
	if err != nil {
		return "", nil, err
	}
	m.generation++
	configPath := filepath.Join(m.configDir, fmt.Sprintf("workerd-%06d.capnp", m.generation))
	if err := m.writer.WriteWorkerd(configPath, generation); err != nil {
		if socketDir != "" {
			os.RemoveAll(socketDir)
		}
		return "", nil, err
	}

	process, err := m.launcher.Launch(configPath, generation)
	if err != nil {
		os.Remove(configPath)
		if socketDir != "" {
			os.RemoveAll(socketDir)
		}
		return "", nil, fmt.Errorf("start workerd: %w", err)
	}
	drained := make(chan struct{})
	close(drained)
	next := &pool{configPath: configPath, process: process, active: generation, socketDir: socketDir, drained: drained}
	if err := m.waitHealthy(process, generation); err != nil {
		m.stop(next)
		return "", nil, err
	}
	m.pending = next
	return filepath.Base(configPath), append([]nanoflare.ActiveDeployment(nil), generation...), nil
}

func (m *Manager) commit(generation string) error {
	if m.closed {
		return errors.New("runtime manager is closed")
	}
	if m.pending == nil || poolGeneration(m.pending) != generation {
		return errors.New("runtime generation is not pending")
	}
	next := m.pending
	m.pending = nil
	if err := m.writer.WriteWorkerd(m.canonicalConfig, next.active); err != nil {
		m.stop(next)
		return err
	}
	if next.process == nil {
		m.publish(nil, nil)
		return nil
	}
	m.publish(next, next.active)
	return nil
}

func (m *Manager) abort(generation string) error {
	if m.pending == nil || poolGeneration(m.pending) != generation {
		return errors.New("runtime generation is not pending")
	}
	next := m.pending
	m.pending = nil
	if next.process == nil {
		return nil
	}
	return m.stop(next)
}

func poolGeneration(pool *pool) string {
	if pool.configPath == "" {
		return "empty"
	}
	return filepath.Base(pool.configPath)
}

func (m *Manager) Close() error {
	m.mu.Lock()
	m.closed = true
	previous := m.active
	m.active = nil
	pending := m.pending
	m.pending = nil
	m.mu.Unlock()
	var firstErr error
	if pending != nil && pending.process != nil {
		if err := m.stop(pending); err != nil {
			firstErr = err
		}
	}
	if previous != nil {
		if err := m.stop(previous); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	m.retiring.Wait()
	return firstErr
}

func (m *Manager) publish(next *pool, desired []nanoflare.ActiveDeployment) {
	m.revision++
	previous := m.active
	m.active = next
	if next != nil {
		next.scheduler = startCronRunner(next.active, m.output, m.Ensure)
		go m.watch(next, append([]nanoflare.ActiveDeployment(nil), desired...))
	}
	if previous == nil {
		return
	}
	m.retiring.Add(1)
	go func() {
		defer m.retiring.Done()
		if previous.scheduler != nil {
			previous.scheduler.Stop()
			previous.scheduler = nil
		}
		m.mu.Lock()
		drained := previous.drained
		m.mu.Unlock()
		timer := time.NewTimer(m.stopTimeout)
		defer timer.Stop()
		select {
		case <-drained:
		case <-timer.C:
		}
		if err := m.stop(previous); err != nil {
			log.Printf("stop previous workerd generation: %v", err)
		}
	}()
}

func (m *Manager) stop(pool *pool) error {
	if pool.scheduler != nil {
		pool.scheduler.Stop()
	}
	if pool.process == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), m.stopTimeout)
	defer cancel()
	err := pool.process.Stop(ctx)
	os.Remove(pool.configPath)
	if pool.socketDir != "" {
		os.RemoveAll(pool.socketDir)
	}
	return err
}

func (m *Manager) runtimeEndpoints(active []nanoflare.ActiveDeployment) ([]nanoflare.ActiveDeployment, string, error) {
	if m.socketDir == "" {
		return nil, "", errors.New("runtime socket directory is required")
	}
	root, err := filepath.Abs(m.socketDir)
	if err != nil {
		return nil, "", err
	}
	if err := os.MkdirAll(root, 0700); err != nil {
		return nil, "", err
	}
	dir, err := os.MkdirTemp(root, "g-")
	if err != nil {
		return nil, "", err
	}
	items := append([]nanoflare.ActiveDeployment(nil), active...)
	for i := range items {
		items[i].Deployment.Port = 0
		items[i].SocketPath = filepath.Join(dir, fmt.Sprintf("w%d.sock", i))
		if len(items[i].SocketPath) > 100 {
			os.RemoveAll(dir)
			return nil, "", errors.New("runtime socket path exceeds 100 bytes; use a shorter socket directory")
		}
	}
	return items, dir, nil
}

func (m *Manager) waitHealthy(process Process, active []nanoflare.ActiveDeployment) error {
	ctx, cancel := context.WithTimeout(context.Background(), m.healthTimeout)
	defer cancel()
	interval := runtimeHealthCheckInitialInterval
	timer := time.NewTimer(interval)
	defer timer.Stop()
	for {
		select {
		case err := <-process.Done():
			return fmt.Errorf("workerd exited before readiness: %v", err)
		default:
		}
		if socketsReady(active) {
			return nil
		}
		select {
		case err := <-process.Done():
			if err == nil {
				err = errors.New("workerd exited")
			}
			return fmt.Errorf("workerd failed before becoming healthy: %w", err)
		case <-ctx.Done():
			return fmt.Errorf("workerd health check: %w", ctx.Err())
		case <-timer.C:
			interval = min(interval*2, runtimeHealthCheckMaxInterval)
			timer.Reset(interval)
		}
	}
}

func (m *Manager) watch(pool *pool, desired []nanoflare.ActiveDeployment) {
	err := <-pool.process.Done()
	m.mu.Lock()
	if m.closed || m.active != pool {
		m.mu.Unlock()
		return
	}
	m.active = nil
	revision := m.revision
	autoRestart := m.autoRestart
	m.mu.Unlock()
	if pool.scheduler != nil {
		pool.scheduler.Stop()
	}
	os.Remove(pool.configPath)
	if pool.socketDir != "" {
		os.RemoveAll(pool.socketDir)
	}
	log.Printf("active workerd generation exited unexpectedly: %v", err)
	if !autoRestart {
		return
	}
	for {
		time.Sleep(m.restartDelay)
		m.mu.Lock()
		if m.closed || m.active != nil || m.revision != revision {
			m.mu.Unlock()
			return
		}
		err := m.write(desired)
		m.mu.Unlock()
		if err != nil {
			log.Printf("restart workerd generation: %v", err)
			continue
		}
		return
	}
}

func socketsReady(active []nanoflare.ActiveDeployment) bool {
	for _, item := range active {
		if item.SocketPath == "" {
			return false
		}
		// Connecting and closing immediately can race peer credential lookup on
		// macOS. Fresh, private generation directories exclude stale socket files.
		info, err := os.Stat(item.SocketPath)
		if err != nil || info.Mode()&os.ModeSocket == 0 {
			return false
		}
	}
	return true
}

type CommandLauncher struct {
	Executable   string
	Experimental bool
	Output       *OutputBuffer
}

func (l CommandLauncher) Launch(configPath string, active []nanoflare.ActiveDeployment) (Process, error) {
	args := []string{"serve", configPath}
	if l.Experimental {
		args = append(args, "--experimental")
	}
	command := exec.Command(l.Executable, args...)
	command.Env = minimalEnvironment()
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	command.Stdout = os.Stdout
	command.Stderr = os.Stderr
	if l.Output != nil {
		command.Stdout = io.MultiWriter(os.Stdout, l.Output)
		command.Stderr = io.MultiWriter(os.Stderr, l.Output)
		message := fmt.Sprintf("starting workerd generation from %s", filepath.Base(configPath))
		if len(active) == 1 {
			l.Output.AppendActive(active[0], "info", message)
		} else {
			l.Output.Append("info", message)
		}
	}
	if err := command.Start(); err != nil {
		return nil, err
	}
	log.Printf("started workerd pid=%d config=%s", command.Process.Pid, configPath)
	process := &commandProcess{command: command, done: make(chan error, 1)}
	go func() {
		process.done <- command.Wait()
		close(process.done)
	}()
	return process, nil
}

func minimalEnvironment() []string {
	result := []string{"TZ=UTC"}
	if path := os.Getenv("PATH"); path != "" {
		result = append(result, "PATH="+path)
	}
	return result
}

type commandProcess struct {
	command *exec.Cmd
	done    chan error
}

func (p *commandProcess) Done() <-chan error {
	return p.done
}

func (p *commandProcess) Stop(ctx context.Context) error {
	if err := syscall.Kill(-p.command.Process.Pid, syscall.SIGINT); err != nil && !errors.Is(err, syscall.ESRCH) {
		return err
	}
	select {
	case <-p.done:
		return nil
	case <-ctx.Done():
		if err := syscall.Kill(-p.command.Process.Pid, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
			return err
		}
		<-p.done
		return ctx.Err()
	}
}
