package runtime

import (
	"context"
	"errors"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/clas/nanoflare/internal/nanoflare"
)

func TestMinimalEnvironmentDoesNotForwardNanoflareSecrets(t *testing.T) {
	t.Setenv("PATH", "/usr/bin")
	t.Setenv("DATABASE_URL", "postgres://secret")
	t.Setenv("MINIO_SECRET_KEY", "secret")

	got := minimalEnvironment()
	want := []string{"TZ=UTC", "PATH=/usr/bin"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("minimalEnvironment() = %#v, want %#v", got, want)
	}
}

func TestCommandLauncherScopesSingleDeploymentStartupOutput(t *testing.T) {
	executable, err := exec.LookPath("true")
	if err != nil {
		t.Skip("true command is not available")
	}
	output := NewOutputBuffer()
	process, err := CommandLauncher{Executable: executable, Output: output}.Launch("workerd-test.capnp", []nanoflare.ActiveDeployment{{
		App:        nanoflare.App{ID: "app-1"},
		Deployment: nanoflare.Deployment{ID: "dep-1"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	<-process.Done()

	lines := output.Output("app-1")
	if len(lines) != 1 {
		t.Fatalf("got %d lines, want 1: %#v", len(lines), lines)
	}
	if lines[0].DeploymentID != "dep-1" || lines[0].Message != "starting workerd generation from workerd-test.capnp" {
		t.Fatalf("unexpected output line: %#v", lines[0])
	}
}

func TestManagerReplacesHealthyPoolBeforePublishingTraffic(t *testing.T) {
	events := &eventLog{}
	writer := &fakeWriter{events: events}
	launcher := &fakeLauncher{events: events, healthy: true}
	manager := NewManager(writer, launcher, t.TempDir(), filepath.Join(t.TempDir(), "workerd.capnp"), testSocketDir(t), time.Second, time.Second)
	defer manager.Close()

	first := deployments(9001)
	if err := manager.Write(first); err != nil {
		t.Fatal(err)
	}
	firstSocket := writer.runtimeSocket(0)
	if firstSocket == "" {
		t.Fatal("runtime endpoint has no socket")
	}

	if err := manager.Write(deployments(9002)); err != nil {
		t.Fatal(err)
	}
	secondSocket := writer.runtimeSocket(1)
	if secondSocket == firstSocket {
		t.Fatal("replacement reused the active generation socket")
	}

	manager.retiring.Wait()
	if !launcher.process(0).isStopped() {
		t.Fatal("previous pool was not stopped")
	}
	if got := events.values(); index(got, "traefik") > index(got, "stop") {
		t.Fatalf("events = %#v, want Traefik publication before previous pool stop", got)
	}
}

func TestManagerKeepsPreviousPoolWhenReplacementIsUnhealthy(t *testing.T) {
	writer := &fakeWriter{}
	launcher := &fakeLauncher{healthy: true}
	manager := NewManager(writer, launcher, t.TempDir(), filepath.Join(t.TempDir(), "workerd.capnp"), testSocketDir(t), 75*time.Millisecond, time.Second)
	defer manager.Close()

	if err := manager.Write(deployments(9001)); err != nil {
		t.Fatal(err)
	}
	launcher.healthy = false
	if err := manager.Write(deployments(9002)); err == nil {
		t.Fatal("Write() error = nil, want unhealthy replacement error")
	}
	if launcher.process(0).isStopped() {
		t.Fatal("previous healthy pool was stopped")
	}
	if !launcher.process(1).isStopped() {
		t.Fatal("unhealthy replacement pool was not stopped")
	}
	if writer.traefikCount() != 1 {
		t.Fatalf("Traefik writes = %d, want 1", writer.traefikCount())
	}
}

func TestManagerKeepsPreviousPoolUntilPreparedGenerationIsCommitted(t *testing.T) {
	writer := &fakeWriter{}
	launcher := &fakeLauncher{healthy: true}
	manager := NewManager(writer, launcher, t.TempDir(), filepath.Join(t.TempDir(), "workerd.capnp"), testSocketDir(t), time.Second, time.Second)
	defer manager.Close()

	if err := manager.Write(deployments(9001)); err != nil {
		t.Fatal(err)
	}
	generation, _, err := manager.Prepare(deployments(9002))
	if err != nil {
		t.Fatal(err)
	}
	if launcher.process(0).isStopped() {
		t.Fatal("previous pool stopped before commit")
	}
	if err := manager.Commit(generation); err != nil {
		t.Fatal(err)
	}
	manager.retiring.Wait()
	if !launcher.process(0).isStopped() {
		t.Fatal("previous pool remains active after commit")
	}
}

func TestManagerRestartsUnexpectedlyExitedPool(t *testing.T) {
	writer := &fakeWriter{}
	launcher := &fakeLauncher{healthy: true}
	manager := NewManager(writer, launcher, t.TempDir(), filepath.Join(t.TempDir(), "workerd.capnp"), testSocketDir(t), time.Second, time.Second)
	manager.restartDelay = time.Millisecond
	defer manager.Close()

	if err := manager.Write(deployments(9001)); err != nil {
		t.Fatal(err)
	}
	launcher.process(0).crash(errors.New("process exited"))
	deadline := time.Now().Add(time.Second)
	for (launcher.count() != 2 || writer.traefikCount() != 2) && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if launcher.count() != 2 {
		t.Fatalf("launches = %d, want automatic replacement", launcher.count())
	}
	if writer.traefikCount() != 2 {
		t.Fatalf("Traefik writes = %d, want replacement publication", writer.traefikCount())
	}
}

func TestManagerCanDisableAutomaticRestart(t *testing.T) {
	writer := &fakeWriter{}
	launcher := &fakeLauncher{healthy: true}
	manager := NewManager(writer, launcher, t.TempDir(), filepath.Join(t.TempDir(), "workerd.capnp"), testSocketDir(t), time.Second, time.Second)
	manager.SetAutoRestart(false)
	defer manager.Close()

	if err := manager.Write(deployments(9001)); err != nil {
		t.Fatal(err)
	}
	launcher.process(0).crash(errors.New("process exited"))
	time.Sleep(10 * time.Millisecond)
	if launcher.count() != 1 {
		t.Fatalf("launches = %d, want no automatic replacement", launcher.count())
	}
}

func deployments(port int) []nanoflare.ActiveDeployment {
	return []nanoflare.ActiveDeployment{{
		App: nanoflare.App{ID: "hello", Hostname: "hello.example.com"},
		Deployment: nanoflare.Deployment{
			ID:                "d782fdf238391fd3ea15d629e16ff825fb19332a50347e04",
			AppID:             "hello",
			Files:             []nanoflare.WorkerFile{{Path: "worker.js", Content: `addEventListener("fetch", () => {});`}},
			Entrypoint:        "worker.js",
			CompatibilityDate: "2025-12-10",
			Port:              port,
		},
	}}
}

func testSocketDir(t testing.TB) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "nf-runtime-test-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return dir
}

type fakeWriter struct {
	mu      sync.Mutex
	events  *eventLog
	traefik [][]nanoflare.ActiveDeployment
}

func (w *fakeWriter) WriteWorkerd(_ string, _ []nanoflare.ActiveDeployment) error {
	w.events.add("workerd")
	return nil
}

func (w *fakeWriter) WriteTraefik(active []nanoflare.ActiveDeployment) error {
	w.events.add("traefik")
	w.mu.Lock()
	defer w.mu.Unlock()
	w.traefik = append(w.traefik, append([]nanoflare.ActiveDeployment(nil), active...))
	return nil
}

func (w *fakeWriter) traefikCount() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return len(w.traefik)
}

func (w *fakeWriter) runtimeSocket(index int) string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.traefik[index][0].SocketPath
}

type fakeLauncher struct {
	mu        sync.Mutex
	events    *eventLog
	healthy   bool
	processes []*fakeProcess
}

func (l *fakeLauncher) Launch(_ string, active []nanoflare.ActiveDeployment) (Process, error) {
	l.events.add("launch")
	process := &fakeProcess{events: l.events, done: make(chan error, 1)}
	if l.healthy {
		for _, item := range active {
			listener, err := net.Listen("unix", item.SocketPath)
			if err != nil {
				return nil, err
			}
			process.listeners = append(process.listeners, listener)
		}
	}
	l.mu.Lock()
	l.processes = append(l.processes, process)
	l.mu.Unlock()
	return process, nil
}

func (l *fakeLauncher) count() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.processes)
}

func (l *fakeLauncher) process(index int) *fakeProcess {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.processes[index]
}

type fakeProcess struct {
	mu        sync.Mutex
	events    *eventLog
	done      chan error
	listeners []net.Listener
	stopped   bool
}

func (p *fakeProcess) Done() <-chan error {
	return p.done
}

func (p *fakeProcess) Stop(context.Context) error {
	return p.finish(nil)
}

func (p *fakeProcess) crash(err error) {
	p.finish(err)
}

func (p *fakeProcess) finish(err error) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.stopped {
		return nil
	}
	p.stopped = true
	p.events.add("stop")
	for _, listener := range p.listeners {
		listener.Close()
	}
	p.done <- err
	close(p.done)
	return nil
}

func (p *fakeProcess) isStopped() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.stopped
}

type eventLog struct {
	mu     sync.Mutex
	events []string
}

func (l *eventLog) add(event string) {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.events = append(l.events, event)
}

func (l *eventLog) values() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.events...)
}

func index(values []string, target string) int {
	for i, value := range values {
		if value == target {
			return i
		}
	}
	return -1
}

func waitFor(t *testing.T, timeout time.Duration, condition func() bool, description string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", description)
}
