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
	output        *OutputBuffer
	scheduler     *cronRunner
	generation    int
	workers       map[string]*lazyWorker
	activeKeys    map[string]bool
	active        []nanoflare.ActiveDeployment
	closed        bool
}

type lazyWorker struct {
	key          string
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
	var output *OutputBuffer
	switch value := launcher.(type) {
	case CommandLauncher:
		output = value.Output
	case *CommandLauncher:
		output = value.Output
	}
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
		output:        output,
		workers:       make(map[string]*lazyWorker),
		activeKeys:    make(map[string]bool),
	}
}

func (m *LazyManager) Write(active []nanoflare.ActiveDeployment) error {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return errors.New("runtime manager is closed")
	}
	// A lazy workerd process can now contain a caller and its service-binding
	// dependencies. Stop existing processes on a deployment graph change so a
	// caller never keeps pointing at an old target deployment.
	stale := m.staleWorkersLocked(active)
	if !sameActiveDeployments(m.active, active) {
		stale = append(stale, m.allWorkersLocked()...)
	}
	m.active = append([]nanoflare.ActiveDeployment(nil), active...)
	m.activeKeys = activeWorkerKeys(active)
	previousScheduler := m.scheduler
	m.scheduler = nil
	m.mu.Unlock()
	if previousScheduler != nil {
		previousScheduler.Stop()
	}
	for _, worker := range stale {
		m.stopWorker(worker)
	}
	if err := m.writer.WriteTraefik(active); err != nil {
		return err
	}
	nextScheduler := startCronRunnerWithEnsure(m.portHost, active, m.output, m.Ensure)
	if nextScheduler == nil {
		return nil
	}
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		nextScheduler.Stop()
		return nil
	}
	previousScheduler = m.scheduler
	m.scheduler = nextScheduler
	m.mu.Unlock()
	if previousScheduler != nil {
		previousScheduler.Stop()
	}
	return nil
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
		current := m.workers[worker.key]
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
	scheduler := m.scheduler
	m.scheduler = nil
	workers := make([]*lazyWorker, 0, len(m.workers))
	for _, worker := range m.workers {
		workers = append(workers, worker)
	}
	m.workers = make(map[string]*lazyWorker)
	m.activeKeys = make(map[string]bool)
	m.mu.Unlock()
	if scheduler != nil {
		scheduler.Stop()
	}
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
	key := workerKey(active)
	if current := m.workers[key]; current != nil {
		if current.deploymentID == active.Deployment.ID {
			return current, false, nil
		}
		delete(m.workers, key)
		go m.stopWorker(current)
	}
	if !m.activeKeys[key] {
		for currentKey, current := range m.workers {
			if current.appID == active.App.ID {
				delete(m.workers, currentKey)
				go m.stopWorker(current)
			}
		}
	}
	worker := &lazyWorker{
		key:          key,
		appID:        active.App.ID,
		deploymentID: active.Deployment.ID,
		starting:     true,
		ready:        make(chan struct{}),
	}
	m.workers[key] = worker
	return worker, true, nil
}

func (m *LazyManager) start(worker *lazyWorker, active nanoflare.ActiveDeployment) {
	m.mu.Lock()
	available := append([]nanoflare.ActiveDeployment(nil), m.active...)
	m.mu.Unlock()
	generation, err := m.withRuntimePorts(lazyRuntimeClosure(active, available))
	if err != nil {
		m.failStart(worker, err)
		return
	}
	routed := generation[0]
	for _, item := range generation {
		if item.App.ID == active.App.ID && item.Deployment.ID == active.Deployment.ID {
			routed = item
			break
		}
	}
	m.mu.Lock()
	m.generation++
	configPath := filepath.Join(m.configDir, fmt.Sprintf("workerd-lazy-%06d-%s-%s.capnp", m.generation, active.App.ID, active.Deployment.ID))
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
	if m.closed || m.workers[worker.key] != worker {
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
	if m.closed || m.workers[worker.key] != worker {
		return errors.New("runtime manager is closed")
	}
	return nil
}

func (m *LazyManager) failStart(worker *lazyWorker, err error) {
	m.mu.Lock()
	worker.startErr = err
	worker.starting = false
	close(worker.ready)
	if m.workers[worker.key] == worker {
		delete(m.workers, worker.key)
	}
	m.mu.Unlock()
}

func (m *LazyManager) release(worker *lazyWorker) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if worker.refs > 0 {
		worker.refs--
	}
	if m.closed || m.workers[worker.key] != worker || worker.refs != 0 {
		return
	}
	worker.idleTimer = time.AfterFunc(m.idleTimeout, func() {
		m.mu.Lock()
		if m.closed || m.workers[worker.key] != worker || worker.refs != 0 {
			m.mu.Unlock()
			return
		}
		delete(m.workers, worker.key)
		m.mu.Unlock()
		if err := m.stopWorker(worker); err != nil {
			log.Printf("stop idle workerd worker %s: %v", worker.appID, err)
		}
	})
}

func (m *LazyManager) watchLazy(worker *lazyWorker) {
	err := <-worker.process.Done()
	m.mu.Lock()
	if m.workers[worker.key] == worker {
		delete(m.workers, worker.key)
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
		wanted[workerKey(item)] = item.Deployment.ID
	}
	var stale []*lazyWorker
	for key, worker := range m.workers {
		if wanted[key] != worker.deploymentID {
			delete(m.workers, key)
			stale = append(stale, worker)
		}
	}
	return stale
}

func (m *LazyManager) allWorkersLocked() []*lazyWorker {
	workers := make([]*lazyWorker, 0, len(m.workers))
	for key, worker := range m.workers {
		delete(m.workers, key)
		workers = append(workers, worker)
	}
	return workers
}

func sameActiveDeployments(left, right []nanoflare.ActiveDeployment) bool {
	if len(left) != len(right) {
		return false
	}
	seen := make(map[string]bool, len(left))
	for _, item := range left {
		seen[workerKey(item)] = true
	}
	for _, item := range right {
		if !seen[workerKey(item)] {
			return false
		}
	}
	return true
}

// lazyRuntimeClosure returns the caller plus every active Worker it can reach
// through a same-organization service binding. workerd needs all of them in
// the one generated config; a single-worker config leaves env.BINDING unset.
func lazyRuntimeClosure(root nanoflare.ActiveDeployment, available []nanoflare.ActiveDeployment) []nanoflare.ActiveDeployment {
	if len(available) == 0 {
		return []nanoflare.ActiveDeployment{root}
	}
	byService := make(map[string]nanoflare.ActiveDeployment, len(available))
	for _, item := range available {
		byService[item.App.OrgID+"\x00"+item.App.Name] = item
	}
	result := []nanoflare.ActiveDeployment{root}
	seen := map[string]bool{workerKey(root): true}
	for index := 0; index < len(result); index++ {
		for _, binding := range result[index].Deployment.Services {
			target, ok := byService[result[index].App.OrgID+"\x00"+binding.Service]
			if !ok || seen[workerKey(target)] {
				continue
			}
			seen[workerKey(target)] = true
			result = append(result, target)
		}
	}
	return result
}

func workerKey(active nanoflare.ActiveDeployment) string {
	return active.App.ID + ":" + active.Deployment.ID
}

func activeWorkerKeys(active []nanoflare.ActiveDeployment) map[string]bool {
	keys := make(map[string]bool, len(active))
	for _, item := range active {
		keys[workerKey(item)] = true
	}
	return keys
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
