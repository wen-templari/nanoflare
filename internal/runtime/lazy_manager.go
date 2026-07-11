package runtime

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/clas/nanoflare/internal/nanoflare"
)

type EnsuredWorker struct {
	Port    int
	Release func()
}

type LazyManager struct {
	mu            sync.Mutex
	writer        ConfigWriter
	launcher      Launcher
	configDir     string
	portHost      string
	portBind      string
	nextPort      int
	healthTimeout time.Duration
	stopTimeout   time.Duration
	idleTimeout   time.Duration
	generation    int
	workers       map[string]*lazyWorker
	closed        bool
}

type lazyWorker struct {
	appID        string
	deploymentID string
	configPath   string
	process      Process
	port         int
	refs         int
	idleTimer    *time.Timer
	starting     bool
	ready        chan struct{}
	startErr     error
}

func NewLazyManager(writer ConfigWriter, launcher Launcher, configDir, portHost string, portStart int, healthTimeout, stopTimeout, idleTimeout time.Duration) *LazyManager {
	return &LazyManager{
		writer:        writer,
		launcher:      launcher,
		configDir:     configDir,
		portHost:      portHost,
		portBind:      "0.0.0.0",
		nextPort:      portStart,
		healthTimeout: healthTimeout,
		stopTimeout:   stopTimeout,
		idleTimeout:   idleTimeout,
		workers:       make(map[string]*lazyWorker),
	}
}

func (m *LazyManager) Write(active []nanoflare.ActiveDeployment) error {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return errors.New("runtime manager is closed")
	}
	stale := m.staleWorkersLocked(active)
	m.mu.Unlock()
	for _, worker := range stale {
		m.stopWorker(worker)
	}
	return m.writer.WriteTraefik(active)
}

func (m *LazyManager) Ensure(ctx context.Context, active nanoflare.ActiveDeployment) (EnsuredWorker, error) {
	for {
		worker, starter, err := m.worker(active)
		if err != nil {
			return EnsuredWorker{}, err
		}
		if starter {
			m.start(worker, active)
		}
		select {
		case <-worker.ready:
		case <-ctx.Done():
			return EnsuredWorker{}, ctx.Err()
		}
		m.mu.Lock()
		if m.closed {
			m.mu.Unlock()
			return EnsuredWorker{}, errors.New("runtime manager is closed")
		}
		if worker.startErr != nil {
			m.mu.Unlock()
			return EnsuredWorker{}, worker.startErr
		}
		current := m.workers[active.App.ID]
		if current != worker {
			m.mu.Unlock()
			continue
		}
		worker.refs++
		if worker.idleTimer != nil {
			worker.idleTimer.Stop()
			worker.idleTimer = nil
		}
		port := worker.port
		m.mu.Unlock()
		return EnsuredWorker{Port: port, Release: func() { m.release(worker) }}, nil
	}
}

func (m *LazyManager) Close() error {
	m.mu.Lock()
	m.closed = true
	workers := make([]*lazyWorker, 0, len(m.workers))
	for _, worker := range m.workers {
		workers = append(workers, worker)
	}
	m.workers = make(map[string]*lazyWorker)
	m.mu.Unlock()
	var firstErr error
	for _, worker := range workers {
		if err := m.stopWorker(worker); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

func (m *LazyManager) worker(active nanoflare.ActiveDeployment) (*lazyWorker, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return nil, false, errors.New("runtime manager is closed")
	}
	if current := m.workers[active.App.ID]; current != nil {
		if current.deploymentID == active.Deployment.ID {
			return current, false, nil
		}
		delete(m.workers, active.App.ID)
		go m.stopWorker(current)
	}
	worker := &lazyWorker{
		appID:        active.App.ID,
		deploymentID: active.Deployment.ID,
		starting:     true,
		ready:        make(chan struct{}),
	}
	m.workers[active.App.ID] = worker
	return worker, true, nil
}

func (m *LazyManager) start(worker *lazyWorker, active nanoflare.ActiveDeployment) {
	generation, err := m.withRuntimePorts([]nanoflare.ActiveDeployment{active})
	if err != nil {
		m.failStart(worker, err)
		return
	}
	routed := generation[0]
	m.mu.Lock()
	m.generation++
	configPath := filepath.Join(m.configDir, fmt.Sprintf("workerd-lazy-%06d-%s.capnp", m.generation, active.App.ID))
	m.mu.Unlock()
	if err := m.writer.WriteWorkerd(configPath, generation); err != nil {
		m.failStart(worker, err)
		return
	}
	if err := m.ensureStillCurrent(worker); err != nil {
		os.Remove(configPath)
		m.failStart(worker, err)
		return
	}
	process, err := m.launcher.Launch(configPath, generation)
	if err != nil {
		os.Remove(configPath)
		m.failStart(worker, fmt.Errorf("start workerd: %w", err))
		return
	}
	if err := m.ensureStillCurrent(worker); err != nil {
		_ = m.stopWorker(&lazyWorker{configPath: configPath, process: process})
		m.failStart(worker, err)
		return
	}
	pool := &pool{configPath: configPath, process: process, active: generation}
	if err := m.waitHealthy(process, generation); err != nil {
		_ = m.stopWorker(&lazyWorker{configPath: pool.configPath, process: pool.process})
		m.failStart(worker, err)
		return
	}
	m.mu.Lock()
	if m.closed || m.workers[worker.appID] != worker {
		m.mu.Unlock()
		_ = m.stopWorker(&lazyWorker{configPath: configPath, process: process})
		m.failStart(worker, errors.New("runtime manager is closed"))
		return
	}
	worker.configPath = configPath
	worker.process = process
	worker.port = routed.Deployment.Port
	worker.starting = false
	close(worker.ready)
	m.mu.Unlock()
	go m.watchLazy(worker)
}

func (m *LazyManager) ensureStillCurrent(worker *lazyWorker) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed || m.workers[worker.appID] != worker {
		return errors.New("runtime manager is closed")
	}
	return nil
}

func (m *LazyManager) failStart(worker *lazyWorker, err error) {
	m.mu.Lock()
	worker.startErr = err
	worker.starting = false
	close(worker.ready)
	if m.workers[worker.appID] == worker {
		delete(m.workers, worker.appID)
	}
	m.mu.Unlock()
}

func (m *LazyManager) release(worker *lazyWorker) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if worker.refs > 0 {
		worker.refs--
	}
	if m.closed || m.workers[worker.appID] != worker || worker.refs != 0 {
		return
	}
	worker.idleTimer = time.AfterFunc(m.idleTimeout, func() {
		m.mu.Lock()
		if m.closed || m.workers[worker.appID] != worker || worker.refs != 0 {
			m.mu.Unlock()
			return
		}
		delete(m.workers, worker.appID)
		m.mu.Unlock()
		if err := m.stopWorker(worker); err != nil {
			log.Printf("stop idle workerd worker %s: %v", worker.appID, err)
		}
	})
}

func (m *LazyManager) watchLazy(worker *lazyWorker) {
	err := <-worker.process.Done()
	m.mu.Lock()
	if m.workers[worker.appID] == worker {
		delete(m.workers, worker.appID)
	}
	m.mu.Unlock()
	if err != nil {
		log.Printf("lazy workerd worker %s exited: %v", worker.appID, err)
	}
	os.Remove(worker.configPath)
}

func (m *LazyManager) stopWorker(worker *lazyWorker) error {
	if worker.idleTimer != nil {
		worker.idleTimer.Stop()
	}
	if worker.process == nil {
		if worker.configPath != "" {
			os.Remove(worker.configPath)
		}
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), m.stopTimeout)
	defer cancel()
	err := worker.process.Stop(ctx)
	os.Remove(worker.configPath)
	return err
}

func (m *LazyManager) staleWorkersLocked(active []nanoflare.ActiveDeployment) []*lazyWorker {
	wanted := make(map[string]string, len(active))
	for _, item := range active {
		wanted[item.App.ID] = item.Deployment.ID
	}
	var stale []*lazyWorker
	for appID, worker := range m.workers {
		if wanted[appID] != worker.deploymentID {
			delete(m.workers, appID)
			stale = append(stale, worker)
		}
	}
	return stale
}

func (m *LazyManager) withRuntimePorts(active []nanoflare.ActiveDeployment) ([]nanoflare.ActiveDeployment, error) {
	result := make([]nanoflare.ActiveDeployment, len(active))
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

func (m *LazyManager) availablePort() (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
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

func (m *LazyManager) waitHealthy(process Process, active []nanoflare.ActiveDeployment) error {
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
