package runtime

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/clas/platform/internal/platform"
)

type ConfigWriter interface {
	WriteWorkerd(string, []platform.ActiveDeployment) error
	WriteTraefik([]platform.ActiveDeployment) error
}

type Process interface {
	Done() <-chan error
	Stop(context.Context) error
}

type Launcher interface {
	Launch(string, []platform.ActiveDeployment) (Process, error)
}

type Manager struct {
	mu              sync.Mutex
	writer          ConfigWriter
	launcher        Launcher
	configDir       string
	canonicalConfig string
	portHost        string
	portBind        string
	nextPort        int
	healthTimeout   time.Duration
	stopTimeout     time.Duration
	retireDelay     time.Duration
	restartDelay    time.Duration
	autoRestart     bool
	generation      int
	active          *pool
	pending         *pool
	closed          bool
}

type pool struct {
	configPath string
	process    Process
	active     []platform.ActiveDeployment
}

func NewManager(writer ConfigWriter, launcher Launcher, configDir, canonicalConfig, portHost string, portStart int, healthTimeout, stopTimeout time.Duration) *Manager {
	return &Manager{
		writer:          writer,
		launcher:        launcher,
		configDir:       configDir,
		canonicalConfig: canonicalConfig,
		portHost:        portHost,
		portBind:        "0.0.0.0",
		nextPort:        portStart,
		healthTimeout:   healthTimeout,
		stopTimeout:     stopTimeout,
		retireDelay:     250 * time.Millisecond,
		restartDelay:    time.Second,
		autoRestart:     true,
	}
}

func (m *Manager) SetRetireDelay(delay time.Duration) {
	m.retireDelay = delay
}

func (m *Manager) SetAutoRestart(enabled bool) {
	m.autoRestart = enabled
}

func (m *Manager) Write(active []platform.ActiveDeployment) error {
	m.mu.Lock()
	defer m.mu.Unlock()
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

func (m *Manager) Prepare(active []platform.ActiveDeployment) (string, []platform.ActiveDeployment, error) {
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

func (m *Manager) prepare(active []platform.ActiveDeployment) (string, []platform.ActiveDeployment, error) {
	if m.closed {
		return "", nil, errors.New("runtime manager is closed")
	}
	if m.pending != nil {
		return "", nil, errors.New("runtime generation is already pending")
	}

	if len(active) == 0 {
		m.pending = &pool{active: []platform.ActiveDeployment{}}
		return "empty", []platform.ActiveDeployment{}, nil
	}

	generation, err := m.withRuntimePorts(active)
	if err != nil {
		return "", nil, err
	}
	m.generation++
	configPath := filepath.Join(m.configDir, fmt.Sprintf("workerd-%06d.capnp", m.generation))
	if err := m.writer.WriteWorkerd(configPath, generation); err != nil {
		return "", nil, err
	}

	process, err := m.launcher.Launch(configPath, generation)
	if err != nil {
		os.Remove(configPath)
		return "", nil, fmt.Errorf("start workerd: %w", err)
	}
	next := &pool{configPath: configPath, process: process, active: generation}
	if err := m.waitHealthy(process, generation); err != nil {
		m.stop(next)
		return "", nil, err
	}
	m.pending = next
	return filepath.Base(configPath), append([]platform.ActiveDeployment(nil), generation...), nil
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
	defer m.mu.Unlock()
	m.closed = true
	previous := m.active
	m.active = nil
	pending := m.pending
	m.pending = nil
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
	return firstErr
}

func (m *Manager) publish(next *pool, desired []platform.ActiveDeployment) {
	previous := m.active
	m.active = next
	if next != nil {
		go m.watch(next, append([]platform.ActiveDeployment(nil), desired...))
	}
	if previous == nil {
		return
	}
	time.Sleep(m.retireDelay)
	if err := m.stop(previous); err != nil {
		log.Printf("stop previous workerd generation: %v", err)
	}
}

func (m *Manager) stop(pool *pool) error {
	if pool.process == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), m.stopTimeout)
	defer cancel()
	err := pool.process.Stop(ctx)
	os.Remove(pool.configPath)
	return err
}

func (m *Manager) withRuntimePorts(active []platform.ActiveDeployment) ([]platform.ActiveDeployment, error) {
	result := make([]platform.ActiveDeployment, len(active))
	copy(result, active)
	for i := range result {
		port, err := m.availablePort()
		if err != nil {
			return nil, err
		}
		result[i].Deployment.Port = port
	}
	return result, nil
}

func (m *Manager) availablePort() (int, error) {
	for port := m.nextPort; port <= 65535; port++ {
		listener, err := net.Listen("tcp", net.JoinHostPort(m.portBind, fmt.Sprint(port)))
		if err != nil {
			continue
		}
		listener.Close()
		m.nextPort = port + 1
		return port, nil
	}
	return 0, errors.New("no runtime ports available")
}

func (m *Manager) waitHealthy(process Process, active []platform.ActiveDeployment) error {
	ctx, cancel := context.WithTimeout(context.Background(), m.healthTimeout)
	defer cancel()
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	for {
		if socketsReady(m.portHost, active) {
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
		case <-ticker.C:
		}
	}
}

func (m *Manager) watch(pool *pool, desired []platform.ActiveDeployment) {
	err := <-pool.process.Done()
	m.mu.Lock()
	if m.closed || m.active != pool {
		m.mu.Unlock()
		return
	}
	m.active = nil
	autoRestart := m.autoRestart
	m.mu.Unlock()
	log.Printf("active workerd generation exited unexpectedly: %v", err)
	if !autoRestart {
		return
	}
	for {
		time.Sleep(m.restartDelay)
		if err := m.Write(desired); err != nil {
			m.mu.Lock()
			closed := m.closed
			m.mu.Unlock()
			if closed {
				return
			}
			log.Printf("restart workerd generation: %v", err)
			continue
		}
		return
	}
}

func socketsReady(host string, active []platform.ActiveDeployment) bool {
	for _, item := range active {
		connection, err := net.DialTimeout("tcp", net.JoinHostPort(host, fmt.Sprint(item.Deployment.Port)), 50*time.Millisecond)
		if err != nil {
			return false
		}
		connection.Close()
	}
	return true
}

type CommandLauncher struct {
	Executable string
	Output     *OutputBuffer
}

func (l CommandLauncher) Launch(configPath string, _ []platform.ActiveDeployment) (Process, error) {
	command := exec.Command(l.Executable, "serve", configPath)
	command.Env = minimalEnvironment()
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	command.Stdout = os.Stdout
	command.Stderr = os.Stderr
	if l.Output != nil {
		command.Stdout = io.MultiWriter(os.Stdout, l.Output)
		command.Stderr = io.MultiWriter(os.Stderr, l.Output)
		l.Output.Append("info", fmt.Sprintf("starting workerd generation from %s", filepath.Base(configPath)))
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
