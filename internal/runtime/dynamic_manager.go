package runtime

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math/big"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/clas/nanoflare/internal/config"
	"github.com/clas/nanoflare/internal/dnsresolver"
	"github.com/clas/nanoflare/internal/nanoflare"
)

// DynamicManager publishes immutable Worker revisions into a stable dispatcher.
// A publication changes admission and routing, never the workerd process.
type DynamicManager struct {
	nextAdmission                                       uint64
	pendingReleases                                     map[string]string
	releaseMu                                           sync.Mutex
	active                                              []nanoflare.ActiveDeployment
	publication                                         sync.Mutex
	mu                                                  sync.Mutex
	writer                                              ConfigWriter
	launcher                                            Launcher
	configDir, canonicalConfig, socketDir               string
	healthTimeout, stopTimeout                          time.Duration
	options                                             config.WorkerdOptions
	compileOptions                                      string
	controlToken, brokerToken, dispatcherToken, session string
	broker                                              *http.Server
	brokerAddr, socketPath, processDir, configPath      string
	process                                             Process
	snapshots                                           map[string]*dynamicSnapshot
	committed                                           map[string]string
	pending                                             *dynamicPublication
	invocations                                         map[string]*dynamicInvocation
	scheduler                                           *cronRunner
	output                                              *OutputBuffer
	client, controlClient                               *http.Client
	staging                                             int
	closed                                              bool
	autoRestart                                         bool
	stop                                                chan struct{}
	background                                          sync.WaitGroup
}
type dynamicSnapshot struct {
	registeredSession string
	registration      string
	registered        bool
	retired           bool
	frozen            *nanoflare.ExecutionSnapshot
	code              config.WorkerCodeSnapshot
	validated         bool
	validationFailed  bool
}
type dynamicPublication struct {
	done   chan struct{}
	id     string
	keys   map[string]string
	active []nanoflare.ActiveDeployment
}
type dynamicInvocation struct {
	key, token, pin string
	root            *dynamicLifetime
	complete        bool
	started         bool
	epoch           uint64
}
type dynamicLifetime struct {
	responseDone bool
	pending      int
	tokens       []string
}

type dynamicRecoveryState struct {
	process Process
	session string
}

func NewDynamicManager(writer ConfigWriter, launcher Launcher, configDir, canonicalConfig, socketDir string, healthTimeout, stopTimeout time.Duration) *DynamicManager {
	m := &DynamicManager{writer: writer, launcher: launcher, configDir: configDir, canonicalConfig: canonicalConfig, socketDir: socketDir, healthTimeout: healthTimeout, stopTimeout: stopTimeout, snapshots: map[string]*dynamicSnapshot{}, committed: map[string]string{}, invocations: map[string]*dynamicInvocation{}, pendingReleases: map[string]string{}, autoRestart: true, stop: make(chan struct{})}
	if w, ok := writer.(interface{ RuntimeOptions() config.WorkerdOptions }); ok {
		m.options = w.RuntimeOptions()
	}
	// Socket policy is executor-local runtime configuration, not customer code.
	// Keep certificate contents and rotations out of registrations and snapshot IDs.
	compileOptions := m.options
	compileOptions.WFPSockets = config.WFPSocketOptions{}
	encoded, _ := json.Marshal(compileOptions)
	m.compileOptions = string(encoded)
	transport := http.DefaultTransport.(*http.Transport).Clone()
	ConfigureTransport(transport)
	m.client = &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	controlTransport := http.DefaultTransport.(*http.Transport).Clone()
	controlTransport.Proxy = nil
	m.controlClient = &http.Client{Transport: controlTransport, Timeout: 15 * time.Second}
	switch l := launcher.(type) {
	case CommandLauncher:
		m.output = l.Output
	case *CommandLauncher:
		m.output = l.Output
	}
	return m
}
func (m *DynamicManager) SetRuntimeControlToken(token string) { m.controlToken = token }
func (m *DynamicManager) SetAutoRestart(enabled bool) {
	m.mu.Lock()
	m.autoRestart = enabled
	m.mu.Unlock()
}
func dynamicIdentity(a nanoflare.ActiveDeployment) string { return a.App.ID + "/" + a.Deployment.ID }

type executionControlError struct {
	operation string
	status    int
	message   string
}

func (e *executionControlError) Error() string {
	return fmt.Sprintf("execution %s: HTTP %d: %s", e.operation, e.status, e.message)
}
func (m *DynamicManager) control(ctx context.Context, operation string, body any, result any) error {
	data, err := json.Marshal(body)
	if err != nil {
		return err
	}
	address := m.options.RuntimeAddr
	if !strings.Contains(address, "://") {
		address = "http://" + address
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(address, "/")+"/internal/runtime/executions/"+operation, bytes.NewReader(data))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Nanoflare-Runner-Token", m.controlToken)
	response, err := m.controlClient.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		message, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
		return &executionControlError{operation: operation, status: response.StatusCode, message: string(message)}
	}
	if result != nil {
		return json.NewDecoder(response.Body).Decode(result)
	}
	io.Copy(io.Discard, response.Body)
	return nil
}
func (m *DynamicManager) openSession() error {
	var response struct {
		Session string `json:"session_id"`
	}
	if err := m.control(context.Background(), "open-session", struct{}{}, &response); err != nil {
		return err
	}
	m.mu.Lock()
	m.session = response.Session
	m.mu.Unlock()
	return nil
}
func (m *DynamicManager) boot() error {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return errors.New("runtime manager is closed")
	}
	if m.process != nil {
		m.mu.Unlock()
		return nil
	}
	m.mu.Unlock()
	if m.controlToken == "" {
		return errors.New("runtime control credential is required")
	}
	if m.broker == nil {
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			return err
		}
		m.brokerToken = rand.Text()
		m.brokerAddr = listener.Addr().String()
		m.broker = &http.Server{Handler: http.HandlerFunc(m.serveBroker), ReadHeaderTimeout: 10 * time.Second}
		go m.broker.Serve(listener)
		if err := m.openSession(); err != nil {
			m.broker.Close()
			m.broker = nil
			return err
		}
		m.background.Add(1)
		go m.heartbeat()
	}
	if err := os.MkdirAll(m.socketDir, 0700); err != nil {
		return err
	}
	dir, err := os.MkdirTemp(m.socketDir, "d-")
	if err != nil {
		return err
	}
	m.processDir = dir
	m.socketPath = filepath.Join(dir, "dispatcher.sock")
	if len(m.socketPath) > 100 {
		os.RemoveAll(dir)
		return errors.New("runtime socket path exceeds 100 bytes; use a shorter socket directory")
	}
	m.dispatcherToken = rand.Text()
	if err := os.MkdirAll(m.configDir, 0700); err != nil {
		return err
	}
	m.configPath = filepath.Join(m.configDir, "dynamic-dispatcher.capnp")
	if err := os.WriteFile(m.configPath, []byte(config.DynamicDispatcher(m.options, m.socketPath, m.brokerAddr, m.brokerToken, m.dispatcherToken)), 0600); err != nil {
		return err
	}
	launcher := m.launcher
	switch l := launcher.(type) {
	case CommandLauncher:
		l.Experimental = true
		launcher = l
	case *CommandLauncher:
		copy := *l
		copy.Experimental = true
		launcher = copy
	}
	process, err := launcher.Launch(m.configPath, nil)
	if err != nil {
		os.RemoveAll(dir)
		return err
	}
	helper := Manager{healthTimeout: m.healthTimeout}
	if err := helper.waitHealthy(process, []nanoflare.ActiveDeployment{{SocketPath: m.socketPath}}); err != nil {
		ctx, cancel := context.WithTimeout(context.Background(), m.stopTimeout)
		defer cancel()
		process.Stop(ctx)
		os.RemoveAll(dir)
		return err
	}
	m.mu.Lock()
	m.process = process
	for _, s := range m.snapshots {
		s.validated = false
	}
	m.mu.Unlock()
	m.background.Add(1)
	go m.watchDynamic(process, dir)
	return nil
}
func (m *DynamicManager) heartbeat() {
	defer m.background.Done()
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-m.stop:
			return
		case <-ticker.C:
			m.mu.Lock()
			observed := dynamicRecoveryState{process: m.process, session: m.session}
			m.mu.Unlock()
			if err := m.control(context.Background(), "heartbeat", map[string]string{"session_id": observed.session}, nil); err != nil {
				log.Printf("runtime session heartbeat: %v", err)
				if recoveryErr := m.recoverSession(&observed); recoveryErr != nil {
					log.Printf("recover runtime session: %v", recoveryErr)
				}
			} else {
				m.flushReleases()
				m.collectSnapshots()
			}
		}
	}
}

// A lost control session invalidates live capability tokens. Stop its runtime
// before reopening admission; never continue an old invocation under new grants.
// Only recover the observed state. On failure, refresh it under the publication
// lock so a retry owns the partial state left by this attempt, not a replacement
// installed by another publisher or recovery after this attempt returns.
func (m *DynamicManager) recoverSession(observed *dynamicRecoveryState) (err error) {
	m.publication.Lock()
	defer m.publication.Unlock()
	return m.recoverSessionLocked(observed)
}

// Caller holds publication so restore and heartbeat cannot rebuild competing
// sessions or publish candidates against a session being replaced.
func (m *DynamicManager) recoverSessionLocked(observed *dynamicRecoveryState) (err error) {
	m.mu.Lock()
	if m.closed || m.process != observed.process || m.session != observed.session {
		m.mu.Unlock()
		return nil
	}
	defer func() {
		if err != nil {
			m.mu.Lock()
			*observed = dynamicRecoveryState{process: m.process, session: m.session}
			m.mu.Unlock()
		}
	}()
	previousSession, previousDir, process := m.session, m.processDir, m.process
	active := append([]nanoflare.ActiveDeployment(nil), m.active...)
	m.process, m.session = nil, ""
	m.invocations = map[string]*dynamicInvocation{}
	m.clearPendingLocked()
	m.mu.Unlock()
	if process != nil {
		ctx, cancel := context.WithTimeout(context.Background(), m.stopTimeout)
		err := process.Stop(ctx)
		cancel()
		if err != nil {
			// Keep a failed stop reachable for shutdown or the next recovery.
			// Admission remains disabled because its session was cleared above.
			m.mu.Lock()
			m.process = process
			m.mu.Unlock()
			return fmt.Errorf("stop expired runtime: %w", err)
		}
	}
	if previousDir != "" {
		os.RemoveAll(previousDir)
	}
	if previousSession != "" {
		m.control(context.Background(), "close-session", map[string]string{"session_id": previousSession}, nil)
	}
	if err := m.openSession(); err != nil {
		return err
	}
	success := false
	defer func() {
		if !success {
			m.mu.Lock()
			session := m.session
			m.session = ""
			m.mu.Unlock()
			m.control(context.Background(), "close-session", map[string]string{"session_id": session}, nil)
		}
	}()
	m.mu.Lock()
	m.snapshots = map[string]*dynamicSnapshot{}
	m.committed = map[string]string{}
	m.mu.Unlock()
	id, routed, err := m.prepareDynamic(active, true, "")
	if err != nil {
		return err
	}
	if err := m.writer.WriteTraefik(routed); err != nil {
		return err
	}
	if err := m.commitDynamic(id); err != nil {
		return err
	}
	success = true
	return nil
}

func (m *DynamicManager) register(snapshot *dynamicSnapshot) error {
	active := snapshot.frozen.ActiveDeployment()
	var response struct {
		ID         string `json:"execution_id"`
		SnapshotID string `json:"snapshot_id"`
	}
	if err := m.control(context.Background(), "register", map[string]any{
		"active": active, "secret_values": active.App.SecretValues, "object_key": active.Deployment.ObjectKey,
		"compile_options": m.compileOptions, "session_id": snapshot.registeredSession, "registration_id": snapshot.registration,
	}, &response); err != nil {
		return err
	}
	if response.ID != nanoflare.ResourceGrantID(snapshot.registeredSession, snapshot.registration) || response.SnapshotID != snapshot.frozen.ID() {
		return errors.New("resource grant identity mismatch")
	}
	return nil
}
func (m *DynamicManager) build(active []nanoflare.ActiveDeployment, restore bool, strictAppID string) (map[string]string, error) {
	active = staticRuntimeWorkers(active)
	m.mu.Lock()
	m.staging++
	m.mu.Unlock()
	defer func() { m.mu.Lock(); m.staging--; m.mu.Unlock() }()
	if validator, ok := m.writer.(interface {
		ValidateRuntime([]nanoflare.ActiveDeployment) error
	}); ok {
		if err := validator.ValidateRuntime(active); err != nil {
			return nil, err
		}
	}
	keys := map[string]string{}
	for _, item := range active {
		snapshot, err := nanoflare.FreezeExecution(item, m.compileOptions)
		if err != nil {
			return nil, err
		}
		m.mu.Lock()
		session := m.session
		var key string
		var existing *dynamicSnapshot
		for candidateKey, candidate := range m.snapshots {
			if candidate.frozen.ID() == snapshot.ID() && candidate.registeredSession == session && !candidate.retired {
				key, existing = candidateKey, candidate
				break
			}
		}
		m.mu.Unlock()
		if existing == nil {
			code := config.InstrumentLoaderInvocation(config.SnapshotWorkerCodeForLoader(snapshot.ActiveDeployment(), m.options), snapshot.ActiveDeployment())
			if _, err := json.Marshal(code); err != nil {
				return nil, err
			}
			registration := rand.Text()
			key = nanoflare.ResourceGrantID(session, registration)
			existing = &dynamicSnapshot{frozen: snapshot, code: code, registration: registration, registeredSession: session}
			m.mu.Lock()
			m.snapshots[key] = existing
			m.mu.Unlock()
		}
		if !existing.registered {
			if err := m.register(existing); err != nil {
				return nil, err
			}
			m.mu.Lock()
			existing.registered = true
			m.mu.Unlock()
		}
		m.mu.Lock()
		validated := existing.validated
		previous := m.snapshots[m.committed[dynamicIdentity(item)]]
		allowFailure := restore || (strictAppID != "" && item.App.ID != strictAppID && previous != nil && previous.validationFailed && previous.frozen.ID() == snapshot.ID())
		m.mu.Unlock()
		if !validated {
			request, _ := http.NewRequest(http.MethodPost, "http://"+SocketHost(m.socketPath)+"/__nanoflare_validate", nil)
			request.Header.Set("X-Nanoflare-Dispatcher-Token", m.dispatcherToken)
			request.Header.Set("X-Nanoflare-Execution", key)
			request.Header.Set("X-Nanoflare-Validate", "true")
			ctx, cancel := context.WithTimeout(context.Background(), m.healthTimeout)
			request = request.WithContext(ctx)
			response, err := m.client.Do(request)
			if err != nil {
				cancel()
				return nil, fmt.Errorf("validate Worker: %w", err)
			}
			io.Copy(io.Discard, io.LimitReader(response.Body, 8192))
			response.Body.Close()
			cancel()
			if response.StatusCode != http.StatusNoContent {
				if response.StatusCode != http.StatusUnprocessableEntity || response.Header.Get("X-Nanoflare-Validation-Failure") != "worker_initialization" {
					return nil, fmt.Errorf("validate Worker %s: HTTP %d", item.App.Name, response.StatusCode)
				}
				if !allowFailure {
					return nil, &WorkerUnavailableError{AppID: item.App.ID, DeploymentID: item.Deployment.ID}
				}
				log.Printf("Worker unavailable: app=%s revision=%s reason=worker_initialization", item.App.ID, item.Deployment.ID)
			}
			m.mu.Lock()
			existing.validationFailed = response.StatusCode != http.StatusNoContent
			existing.validated = true
			m.mu.Unlock()
		}
		m.mu.Lock()
		failed := existing.validationFailed
		m.mu.Unlock()
		if failed && !allowFailure {
			return nil, &WorkerUnavailableError{AppID: item.App.ID, DeploymentID: item.Deployment.ID}
		}
		keys[dynamicIdentity(item)] = key
	}
	return keys, nil
}
func (m *DynamicManager) ValidatePublication(active []nanoflare.ActiveDeployment) error {
	return m.ValidateWorkerPublication(active, "")
}

func (m *DynamicManager) ValidateWorkerPublication(active []nanoflare.ActiveDeployment, appID string) error {
	m.publication.Lock()
	defer m.publication.Unlock()
	active = staticRuntimeWorkers(active)
	m.mu.Lock()
	empty := len(active) == 0 && m.process == nil
	m.mu.Unlock()
	if empty {
		return nil
	}
	if err := m.boot(); err != nil {
		return err
	}
	_, err := m.build(active, false, appID)
	if err != nil {
		m.collectSnapshots()
	}
	return err
}
func (m *DynamicManager) Prepare(active []nanoflare.ActiveDeployment) (string, []nanoflare.ActiveDeployment, error) {
	m.publication.Lock()
	defer m.publication.Unlock()
	id, routed, err := m.prepareDynamic(active, false, "")
	if err != nil {
		m.collectSnapshots()
	}
	return id, routed, err
}
func (m *DynamicManager) prepareDynamic(active []nanoflare.ActiveDeployment, restore bool, strictAppID string) (string, []nanoflare.ActiveDeployment, error) {
	m.mu.Lock()
	observed := dynamicRecoveryState{process: m.process, session: m.session}
	m.mu.Unlock()
	if observed.session == "" && m.broker != nil {
		// A failed reconnect can retain the broker after clearing admission.
		// boot only opens a session when creating a broker, so retry recovery
		// here before any snapshot can register under an empty session.
		if err := m.recoverSessionLocked(&observed); err != nil {
			return "", nil, fmt.Errorf("reopen runtime session: %w", err)
		}
	}
	if observed.session != "" {
		// A restarted control process has no record of the retained runner's
		// session. Recover synchronously before cached snapshots can reach
		// commit; waiting for the periodic heartbeat can make recovery fail.
		if err := m.control(context.Background(), "heartbeat", map[string]string{"session_id": observed.session}, nil); err != nil {
			var responseError *executionControlError
			if !errors.As(err, &responseError) || responseError.status != http.StatusForbidden {
				return "", nil, err
			}
			if err := m.recoverSessionLocked(&observed); err != nil {
				return "", nil, fmt.Errorf("restore runtime session: %w", err)
			}
		}
	}
	active = staticRuntimeWorkers(active)
	m.mu.Lock()
	// Retain candidates until pending owns them, including the detached routing
	// copy after build returns. Cleanup can run independently of publication.
	m.staging++
	defer func() { m.mu.Lock(); m.staging--; m.mu.Unlock() }()
	pending := m.pending != nil
	m.mu.Unlock()
	if pending {
		return "", nil, errors.New("runtime publication already pending")
	}
	if len(active) == 0 {
		m.mu.Lock()
		if m.process == nil {
			id := rand.Text()
			m.pending = &dynamicPublication{id: id, done: make(chan struct{}), keys: map[string]string{}}
			m.mu.Unlock()
			return id, nil, nil
		}
		m.mu.Unlock()
	}
	if err := m.boot(); err != nil {
		return "", nil, err
	}
	keys, err := m.build(active, restore, strictAppID)
	if err != nil {
		return "", nil, err
	}
	routed := make([]nanoflare.ActiveDeployment, len(active))
	for i, item := range active {
		detached, err := nanoflare.FreezeExecution(item, m.compileOptions)
		if err != nil {
			return "", nil, err
		}
		routed[i] = detached.ActiveDeployment()
		routed[i].SocketPath = m.socketPath
		routed[i].Deployment.Port = 0
	}
	id := rand.Text()
	m.mu.Lock()
	m.pending = &dynamicPublication{id: id, done: make(chan struct{}), keys: keys, active: routed}
	m.mu.Unlock()
	return id, routed, nil
}
func (m *DynamicManager) Commit(id string) error {
	m.publication.Lock()
	defer m.publication.Unlock()
	return m.commitDynamic(id)
}
func (m *DynamicManager) commitDynamic(id string) error {
	m.mu.Lock()
	if m.closed || m.pending == nil || m.pending.id != id {
		m.mu.Unlock()
		return errors.New("runtime publication is not pending")
	}
	publication := m.pending
	session := m.session
	m.mu.Unlock()
	for _, key := range publication.keys {
		m.mu.Lock()
		failed := m.snapshots[key].validationFailed
		m.mu.Unlock()
		if failed {
			continue
		}
		if err := m.control(context.Background(), "publish", map[string]string{"execution_id": key, "session_id": session}, nil); err != nil {
			m.mu.Lock()
			m.clearPendingLocked()
			m.mu.Unlock()
			m.collectSnapshots()
			return err
		}
	}
	m.mu.Lock()
	m.clearPendingLocked()
	previous := m.committed
	m.committed = publication.keys
	m.active = publication.active
	scheduler := m.scheduler
	m.mu.Unlock()
	runnable := make([]nanoflare.ActiveDeployment, 0, len(publication.active))
	m.mu.Lock()
	for _, item := range publication.active {
		if snapshot := m.snapshots[publication.keys[dynamicIdentity(item)]]; snapshot != nil && !snapshot.validationFailed {
			runnable = append(runnable, item)
		}
	}
	m.mu.Unlock()
	if scheduler != nil {
		scheduler.Update(runnable)
	} else {
		scheduler = startCronRunner(runnable, m.output, m.Ensure)
		m.mu.Lock()
		m.scheduler = scheduler
		m.mu.Unlock()
	}
	retained := map[string]bool{}
	for _, key := range publication.keys {
		retained[key] = true
	}
	for _, key := range previous {
		if !retained[key] {
			m.mu.Lock()
			snapshot := m.snapshots[key]
			if snapshot != nil {
				snapshot.retired = true
			}
			m.mu.Unlock()
			if err := m.control(context.Background(), "retire", map[string]string{"execution_id": key, "session_id": session}, nil); err != nil {
				return err
			}
		}
	}
	m.collectSnapshots()
	return nil
}
func (m *DynamicManager) Abort(id string) error {
	m.publication.Lock()
	defer m.publication.Unlock()
	m.mu.Lock()
	if m.pending == nil || m.pending.id != id {
		m.mu.Unlock()
		return errors.New("runtime publication is not pending")
	}
	m.clearPendingLocked()
	m.mu.Unlock()
	m.collectSnapshots()
	return nil
}
func (m *DynamicManager) Write(active []nanoflare.ActiveDeployment) error {
	m.publication.Lock()
	defer m.publication.Unlock()
	id, routed, err := m.prepareDynamic(active, true, "")
	if err != nil {
		return err
	}
	if err := m.writer.WriteTraefik(routed); err != nil {
		m.mu.Lock()
		m.clearPendingLocked()
		m.mu.Unlock()
		return err
	}
	return m.commitDynamic(id)
}
func (m *DynamicManager) admitLocked(ctx context.Context, key string, root *dynamicLifetime) (*dynamicInvocation, error) {
	if m.closed || m.process == nil || m.session == "" {
		return nil, errors.New("runtime unavailable")
	}
	if snapshot := m.snapshots[key]; snapshot != nil && snapshot.validationFailed {
		active := snapshot.frozen.ActiveDeployment()
		return nil, &WorkerUnavailableError{AppID: active.App.ID, DeploymentID: active.Deployment.ID}
	}
	var admission struct {
		Token string `json:"token"`
		Pin   string `json:"pin_id"`
	}
	// m.mu serializes admission with retirement and attaching child lifetimes.
	m.nextAdmission++
	sequence := strconv.FormatUint(m.nextAdmission, 10)
	knownPin := m.session + ":" + sequence
	if err := m.control(ctx, "admit", map[string]string{"execution_id": key, "session_id": m.session, "admission_id": sequence}, &admission); err != nil {
		if m.pendingReleases == nil {
			m.pendingReleases = map[string]string{}
		}
		m.pendingReleases[knownPin] = m.session
		return nil, err
	}
	if admission.Pin != knownPin {
		m.pendingReleases[knownPin] = m.session
		return nil, errors.New("execution admission identity mismatch")
	}
	if root == nil {
		root = &dynamicLifetime{}
	}
	invocation := &dynamicInvocation{key: key, token: admission.Token, pin: admission.Pin, root: root}
	root.pending++
	root.tokens = append(root.tokens, admission.Token)
	m.invocations[admission.Token] = invocation
	return invocation, nil
}
func (m *DynamicManager) Ensure(ctx context.Context, active nanoflare.ActiveDeployment) (EnsuredWorker, error) {
	for {
		m.mu.Lock()
		key := m.committed[dynamicIdentity(active)]
		if key == "" && m.pending != nil && m.pending.keys[dynamicIdentity(active)] != "" && m.pending.done != nil {
			done := m.pending.done
			m.mu.Unlock()
			select {
			case <-done:
				continue
			case <-ctx.Done():
				return EnsuredWorker{}, ctx.Err()
			}
		}
		if key == "" {
			m.mu.Unlock()
			return EnsuredWorker{}, errors.New("deployment is not published")
		}
		invocation, err := m.admitLocked(ctx, key, nil)
		if err != nil {
			m.mu.Unlock()
			m.scheduleReleaseFlush()
			return EnsuredWorker{}, err
		}
		headers := http.Header{}
		headers.Set("X-Nanoflare-Dispatcher-Token", m.dispatcherToken)
		headers.Set("X-Nanoflare-Execution", key)
		headers.Set("X-Nanoflare-Invocation", invocation.token)
		socket := m.socketPath
		m.mu.Unlock()
		var once sync.Once
		return EnsuredWorker{SocketPath: socket, Headers: headers, Release: func() {
			once.Do(func() {
				m.mu.Lock()
				invocation.root.responseDone = true
				pins := m.cancelUnstartedLocked(invocation.root)
				pins = append(pins, m.finishLocked(invocation.root)...)
				m.mu.Unlock()
				m.releasePins(pins)
			})
		}}, nil
	}
}

func (m *DynamicManager) clearPendingLocked() {
	if m.pending != nil && m.pending.done != nil {
		close(m.pending.done)
		m.pending.done = nil
	}
	m.pending = nil
}

// An admission that never reached user code has no background work. Removing
// it under the same lock as /start prevents a late dispatch from acquiring it.
func (m *DynamicManager) cancelUnstartedLocked(root *dynamicLifetime) []string {
	pins := []string{}
	for _, token := range root.tokens[:1] {
		invocation := m.invocations[token]
		if invocation != nil && !invocation.started {
			if !invocation.complete {
				root.pending--
			}
			pins = append(pins, invocation.pin)
			delete(m.invocations, token)
		}
	}
	return pins
}
func (m *DynamicManager) finishLocked(root *dynamicLifetime) []string {
	if !root.responseDone || root.pending != 0 {
		return nil
	}
	pins := []string{}
	for _, token := range root.tokens {
		if invocation := m.invocations[token]; invocation != nil {
			pins = append(pins, invocation.pin)
			delete(m.invocations, token)
		}
	}
	return pins
}
func (m *DynamicManager) releasePins(pins []string) {
	m.mu.Lock()
	if m.pendingReleases == nil {
		m.pendingReleases = map[string]string{}
	}
	for _, pin := range pins {
		session, _, ok := strings.Cut(pin, ":")
		if !ok {
			session = m.session
		}
		m.pendingReleases[pin] = session
	}
	m.mu.Unlock()
	m.flushReleases()
	if len(pins) > 0 {
		m.collectSnapshots()
	}
}
func (m *DynamicManager) scheduleReleaseFlush() {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return
	}
	m.background.Add(1)
	m.mu.Unlock()
	go func() { defer m.background.Done(); m.flushReleases() }()
}
func (m *DynamicManager) flushReleases() {
	m.releaseMu.Lock()
	defer m.releaseMu.Unlock()
	m.mu.Lock()
	pending := make(map[string]string, len(m.pendingReleases))
	for pin, session := range m.pendingReleases {
		pending[pin] = session
	}
	m.mu.Unlock()
	for pin, session := range pending {
		if err := m.control(context.Background(), "release", map[string]string{"pin_id": pin, "session_id": session}, nil); err != nil {
			log.Printf("release execution pin: %v", err)
			continue
		}
		m.mu.Lock()
		delete(m.pendingReleases, pin)
		m.mu.Unlock()
	}
}
func (m *DynamicManager) collectSnapshots() {
	m.mu.Lock()
	if m.staging > 0 {
		m.mu.Unlock()
		return
	}
	retained := map[string]bool{}
	for _, key := range m.committed {
		retained[key] = true
	}
	if m.pending != nil {
		for _, key := range m.pending.keys {
			retained[key] = true
		}
	}
	for _, invocation := range m.invocations {
		retained[invocation.key] = true
	}
	retiring := map[string]*dynamicSnapshot{}
	for key, snapshot := range m.snapshots {
		if !retained[key] {
			snapshot.retired = true
			retiring[key] = snapshot
		}
	}
	m.mu.Unlock()
	// Retiring a revision must not hold the admission mutex while contacting the
	// control plane. A delayed retirement cannot stall unrelated Workers.
	for key, snapshot := range retiring {
		if err := m.control(context.Background(), "retire", map[string]string{"execution_id": key, "session_id": snapshot.registeredSession}, nil); err != nil {
			log.Printf("retire execution: %v", err)
			continue
		}
		m.mu.Lock()
		if m.snapshots[key] == snapshot {
			delete(m.snapshots, key)
		}
		m.mu.Unlock()
	}
}

func (m *DynamicManager) serveBroker(w http.ResponseWriter, r *http.Request) {
	if subtle.ConstantTimeCompare([]byte(r.Header.Get("X-Nanoflare-Broker-Token")), []byte(m.brokerToken)) != 1 {
		http.Error(w, "invalid broker credential", 403)
		return
	}
	parts := strings.SplitN(strings.TrimPrefix(r.URL.Path, "/"), "/", 3)
	if len(parts) < 2 {
		http.NotFound(w, r)
		return
	}
	operation, key := parts[0], parts[1]
	m.mu.Lock()
	snapshot := m.snapshots[key]
	m.mu.Unlock()
	if snapshot == nil {
		http.Error(w, "unknown execution", 404)
		return
	}
	active := snapshot.frozen.ActiveDeployment()
	switch operation {
	case "diagnostic":
		if r.Method != http.MethodPost {
			http.Error(w, "unsupported diagnostic operation", 405)
			return
		}
		var input struct {
			Message string `json:"message"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8192)).Decode(&input); err != nil {
			http.Error(w, "invalid diagnostic", 400)
			return
		}
		if err := m.control(r.Context(), "diagnostic", map[string]string{"session_id": snapshot.registeredSession, "execution_id": key, "message": input.Message}, nil); err != nil {
			http.Error(w, "diagnostic unavailable", 502)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	case "source":
		env := map[string]any{"__NANOFLARE_APP_ID": active.App.ID, "__NANOFLARE_DEPLOYMENT_ID": active.Deployment.ID}
		for name, value := range active.Deployment.Vars {
			var decoded any
			if err := json.Unmarshal(value, &decoded); err != nil {
				http.Error(w, "invalid variable", 500)
				return
			}
			env[name] = decoded
		}
		for name, value := range active.App.SecretValues {
			env[name] = value
		}
		encoded, err := json.Marshal(snapshot.code)
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		var result map[string]any
		json.Unmarshal(encoded, &result)
		mapping := map[string]string{}
		for _, b := range active.Deployment.DispatchNamespaces {
			mapping[b.Binding] = "__NANOFLARE_DISPATCH:" + b.Binding
		}
		env["__NANOFLARE_DISPATCH_BINDINGS"] = mapping
		result["env"] = env
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(result)
	case "start":
		requestedEpoch := uint64(1)
		if value := r.Header.Get("X-Nanoflare-Invocation-Epoch"); value != "" {
			var err error
			requestedEpoch, err = strconv.ParseUint(value, 10, 64)
			if err != nil || requestedEpoch == 0 {
				http.Error(w, "invalid epoch", 400)
				return
			}
		}
		m.mu.Lock()
		invocation := m.invocations[r.Header.Get("X-Nanoflare-Invocation")]
		if invocation == nil || invocation.key != key {
			m.mu.Unlock()
			http.Error(w, "invalid invocation", 403)
			return
		}
		if invocation.started && requestedEpoch == invocation.epoch && !invocation.complete {
			m.mu.Unlock()
			w.WriteHeader(204)
			return
		}
		if invocation.started {
			if !invocation.complete || requestedEpoch != invocation.epoch+1 {
				m.mu.Unlock()
				http.Error(w, "invalid invocation epoch", 409)
				return
			}
			invocation.complete = false
			invocation.root.pending++
		} else if requestedEpoch != 1 {
			m.mu.Unlock()
			http.Error(w, "invalid invocation epoch", 409)
			return
		}
		invocation.started = true
		invocation.epoch = requestedEpoch
		m.mu.Unlock()
		w.WriteHeader(204)
	case "abandon":
		m.mu.Lock()
		invocation := m.invocations[r.Header.Get("X-Nanoflare-Invocation")]
		if invocation == nil {
			m.mu.Unlock()
			w.WriteHeader(204)
			return
		}
		if invocation.key != key {
			m.mu.Unlock()
			http.Error(w, "invalid invocation", 403)
			return
		}
		if !invocation.started && !invocation.complete {
			invocation.complete = true
			invocation.root.pending--
		}
		pins := m.finishLocked(invocation.root)
		m.mu.Unlock()
		m.releasePins(pins)
		w.WriteHeader(204)
	case "complete":
		m.mu.Lock()
		invocation := m.invocations[r.Header.Get("X-Nanoflare-Invocation")]
		if invocation == nil || invocation.key != key {
			m.mu.Unlock()
			http.Error(w, "invalid invocation", 403)
			return
		}
		if epoch := r.Header.Get("X-Nanoflare-Invocation-Epoch"); epoch != "" {
			completedEpoch, err := strconv.ParseUint(epoch, 10, 64)
			if err != nil || completedEpoch == 0 || completedEpoch > invocation.epoch {
				m.mu.Unlock()
				http.Error(w, "invalid invocation epoch", 409)
				return
			}
			if completedEpoch < invocation.epoch {
				m.mu.Unlock()
				w.WriteHeader(204)
				return
			}
		}
		if !invocation.complete {
			invocation.complete = true
			invocation.root.pending--
		}
		pins := m.finishLocked(invocation.root)
		m.mu.Unlock()
		m.releasePins(pins)
		w.WriteHeader(204)
	case "service":
		if len(parts) != 3 {
			http.Error(w, "missing service binding", 400)
			return
		}
		m.mu.Lock()
		caller := m.invocations[r.Header.Get("X-Nanoflare-Invocation")]
		// Callback wrappers retain their originating admission. A revision grant
		// alone cannot stand in for a socket's or request's service-call lineage.
		if caller == nil || caller.key != key || !caller.started {
			m.mu.Unlock()
			http.Error(w, "invalid invocation", 403)
			return
		}
		name, workerID := "", ""
		for _, binding := range active.Deployment.Services {
			if binding.Binding == parts[2] {
				name, workerID = binding.Service, binding.WorkerID
			}
		}
		candidates := []nanoflare.ActiveDeployment{}
		total := 0
		for _, a := range m.active {
			if nanoflare.ServiceTargetAllowed(active.App, a.App) && ((workerID != "" && a.App.ID == workerID) || (workerID == "" && a.App.Platform == nil && a.App.Name == name)) && a.TrafficPercent > 0 {
				candidates = append(candidates, a)
				total += a.TrafficPercent
			}
		}
		targetKey := ""
		if total > 0 {
			choice, err := rand.Int(rand.Reader, big.NewInt(int64(total)))
			if err != nil {
				m.mu.Unlock()
				http.Error(w, "service selection unavailable", 503)
				return
			}
			remaining := int(choice.Int64())
			for _, a := range candidates {
				remaining -= a.TrafficPercent
				if remaining < 0 {
					targetKey = m.committed[dynamicIdentity(a)]
					break
				}
			}
		}
		if (name == "" && workerID == "") || targetKey == "" {
			m.mu.Unlock()
			http.Error(w, "service is not published", 503)
			return
		}
		invocation, err := m.admitLocked(r.Context(), targetKey, caller.root)
		m.mu.Unlock()
		if err != nil {
			m.scheduleReleaseFlush()
			http.Error(w, err.Error(), 503)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"key": targetKey, "token": invocation.token})
	case "export":
		m.mu.Lock()
		caller := m.invocations[r.Header.Get("X-Nanoflare-Invocation")]
		if caller == nil || caller.key != key || !caller.started {
			m.mu.Unlock()
			http.Error(w, "invalid export caller", 403)
			return
		}
		invocation, err := m.admitLocked(r.Context(), key, caller.root)
		m.mu.Unlock()
		if err != nil {
			m.scheduleReleaseFlush()
			http.Error(w, err.Error(), 503)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"key": key, "token": invocation.token})
	case "resource":
		m.forwardResource(w, r, key, active, parts)
	default:
		http.NotFound(w, r)
	}
}
func stripRuntimeHeaders(headers http.Header) {
	for name := range headers {
		if strings.HasPrefix(strings.ToLower(name), "x-nanoflare-") {
			headers.Del(name)
		}
	}
	headers.Del("Authorization")
}
func (m *DynamicManager) forwardResource(w http.ResponseWriter, r *http.Request, key string, active nanoflare.ActiveDeployment, parts []string) {
	binding := r.Header.Get("X-Nanoflare-Binding-Name")
	token := r.Header.Get("X-Nanoflare-Invocation")
	synthetic := binding == "__NANOFLARE_DNS" || binding == "__NANOFLARE_DURATION_COLLECTOR" || binding == "__NANOFLARE_ACCESS" || binding == "__NANOFLARE_LOG_COLLECTOR" || strings.HasPrefix(binding, "__NANOFLARE_DISPATCH:")
	if !synthetic {
		m.mu.Lock()
		snapshot := m.snapshots[key]
		live := false
		for _, committed := range m.committed {
			live = live || committed == key
		}
		for _, invocation := range m.invocations {
			live = live || invocation.key == key
		}
		allowed := live && snapshot != nil && snapshot.registered && snapshot.registeredSession == m.session
		m.mu.Unlock()
		if !allowed {
			http.Error(w, "resource grant is not live", 403)
			return
		}
		token = key
	}
	headers := r.Header.Clone()
	stripRuntimeHeaders(headers)
	if strings.HasPrefix(binding, "__NANOFLARE_DISPATCH:") {
		token = r.Header.Get("X-Nanoflare-Control-Invocation")
		for _, name := range []string{"X-Nanoflare-Invocation", "X-Nanoflare-Outbound-URL", "X-Nanoflare-Outbound-Authorization"} {
			if value := r.Header.Get(name); value != "" {
				headers.Set(name, value)
			}
		}
	}
	headers.Set("Authorization", "Bearer "+token)
	destination := m.options.RuntimeAddr
	path := "/"
	if len(parts) == 3 {
		path += parts[2]
	}
	query := r.URL.RawQuery
	allowed := false
	for _, b := range active.Deployment.KVNamespaces {
		if b.Binding == binding {
			headers.Set("X-Nanoflare-KV-Namespace-ID", b.ID)
			allowed = true
		}
	}
	for _, b := range active.Deployment.Databases {
		if b.Binding == binding {
			headers.Set("X-Nanoflare-Binding", "db")
			headers.Set("X-Nanoflare-Database-ID", b.DatabaseID)
			allowed = true
		}
	}
	for _, b := range active.Deployment.ObjectStorageBuckets {
		if b.Binding == binding {
			headers.Set("X-Nanoflare-Object-Bucket-ID", b.BucketID)
			allowed = true
		}
	}
	assetBinding := active.Deployment.AssetConfig.Binding
	if assetBinding == "" {
		assetBinding = "ASSETS"
	}
	if binding == assetBinding && (len(active.Deployment.Assets) > 0 || active.Deployment.AssetConfig.Binding != "") {
		headers.Set("X-Nanoflare-Binding", "assets")
		headers.Set("X-Nanoflare-Deployment-ID", active.Deployment.ID)
		allowed = true
	}
	if binding == "__NANOFLARE_DNS" && m.options.DNSAddr != "" {
		destination = m.options.DNSAddr
		path = dnsresolver.DoHPath
		headers.Set("X-Nanoflare-DNS-Profile", active.Deployment.DNS.Profile)
		allowed = true
	}
	if binding == "__NANOFLARE_LOG_COLLECTOR" {
		path = "/internal/runtime/logs"
		query = ""
		allowed = true
	}
	if binding == "__NANOFLARE_ACCESS" {
		// Only the access verifier receives the user assertion. Other resource
		// services must never inherit Worker identity headers.
		if assertion := r.Header.Get("X-Nanoflare-User-JWT"); assertion != "" {
			headers.Set("X-Nanoflare-User-JWT", assertion)
		}
		path = "/internal/runtime/access"
		query = ""
		allowed = true
	}
	if strings.HasPrefix(binding, "__NANOFLARE_DISPATCH:") {
		name := strings.TrimPrefix(binding, "__NANOFLARE_DISPATCH:")
		for _, b := range active.Deployment.DispatchNamespaces {
			if b.Binding == name {
				headers.Set("X-Nanoflare-Dispatch-Namespace-ID", b.NamespaceID)
				allowed = true
			}
		}
	}
	if binding == "__NANOFLARE_DURATION_COLLECTOR" {
		path = "/internal/runtime/durations"
		query = ""
		allowed = true
	}
	if !synthetic {
		path = "/internal/runtime/resources" + path
	}
	if !allowed {
		http.Error(w, "undeclared resource binding", 403)
		return
	}
	if !strings.Contains(destination, "://") {
		destination = "http://" + destination
	}
	target, err := url.Parse(destination)
	if err != nil {
		http.Error(w, "invalid runtime address", 500)
		return
	}
	target.Path = path
	target.RawQuery = query
	var body io.Reader = r.Body
	if binding == "__NANOFLARE_DURATION_COLLECTOR" {
		var events []DurationTraceEvent
		if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&events); err != nil {
			http.Error(w, "invalid telemetry", 400)
			return
		}
		for i := range events {
			events[i].ScriptName = active.App.ID
		}
		data, _ := json.Marshal(events)
		body = bytes.NewReader(data)
	}
	request, err := http.NewRequestWithContext(r.Context(), r.Method, target.String(), body)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	request.Header = headers
	response, err := m.client.Do(request)
	if err != nil {
		http.Error(w, err.Error(), 502)
		return
	}
	defer response.Body.Close()
	for name, values := range response.Header {
		for _, value := range values {
			w.Header().Add(name, value)
		}
	}
	w.WriteHeader(response.StatusCode)
	http.NewResponseController(w).Flush()
	io.Copy(dynamicStreamWriter{w}, response.Body)
}
func (m *DynamicManager) watchDynamic(process Process, dir string) {
	defer m.background.Done()
	err := <-process.Done()
	m.mu.Lock()
	if m.process != process {
		m.mu.Unlock()
		return
	}
	m.process = nil
	observed := dynamicRecoveryState{session: m.session}
	pins := []string{}
	for _, invocation := range m.invocations {
		pins = append(pins, invocation.pin)
	}
	m.invocations = map[string]*dynamicInvocation{}
	restart := m.autoRestart && !m.closed
	m.mu.Unlock()
	m.releasePins(pins)
	os.RemoveAll(dir)
	if !restart {
		return
	}
	log.Printf("dynamic dispatcher exited: %v", err)
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-m.stop:
			return
		case <-ticker.C:
			err := m.recoverSession(&observed)
			if err == nil {
				return
			}
			log.Printf("restart dispatcher: %v", err)
		}
	}
}
func (m *DynamicManager) Close() error {
	m.publication.Lock()
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		m.publication.Unlock()
		return nil
	}
	m.closed = true
	m.clearPendingLocked()
	close(m.stop)
	process := m.process
	m.process = nil
	scheduler := m.scheduler
	session := m.session
	m.mu.Unlock()
	m.publication.Unlock()
	if scheduler != nil {
		scheduler.Stop()
	}
	var first error
	if process != nil {
		ctx, cancel := context.WithTimeout(context.Background(), m.stopTimeout)
		first = process.Stop(ctx)
		cancel()
	}
	if m.broker != nil {
		m.broker.Close()
	}
	if session != "" {
		if err := m.control(context.Background(), "close-session", map[string]string{"session_id": session}, nil); first == nil {
			first = err
		}
	}
	m.background.Wait()
	m.mu.Lock()
	m.invocations = map[string]*dynamicInvocation{}
	m.mu.Unlock()
	if m.processDir != "" {
		os.RemoveAll(m.processDir)
	}
	m.client.CloseIdleConnections()
	m.controlClient.CloseIdleConnections()
	return first
}

func (m *DynamicManager) RuntimeDeploymentLive(ctx context.Context, appID, deploymentID string) (bool, error) {
	return m.RuntimeInstanceLive(ctx, appID, deploymentID, "")
}
func (m *DynamicManager) RuntimeInstanceLive(ctx context.Context, appID, deploymentID, instanceID string) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed || m.process == nil || m.session == "" {
		return false, nil
	}
	for key, s := range m.snapshots {
		a := s.frozen.ActiveDeployment()
		if a.App.ID == appID && a.Deployment.ID == deploymentID && (instanceID == "" || key == instanceID) && s.registeredSession == m.session && !s.validationFailed {
			for _, live := range m.committed {
				if live == key {
					return true, nil
				}
			}
			for _, invocation := range m.invocations {
				if invocation.key == key {
					return true, nil
				}
			}
		}
	}
	return false, nil
}

func staticRuntimeWorkers(active []nanoflare.ActiveDeployment) []nanoflare.ActiveDeployment {
	items := make([]nanoflare.ActiveDeployment, 0, len(active))
	for _, a := range active {
		if !a.App.IsDynamic() {
			items = append(items, a)
		}
	}
	return items
}

type dynamicStreamWriter struct{ http.ResponseWriter }

func (w dynamicStreamWriter) Write(data []byte) (int, error) {
	n, err := w.ResponseWriter.Write(data)
	if err == nil {
		err = http.NewResponseController(w.ResponseWriter).Flush()
	}
	return n, err
}
