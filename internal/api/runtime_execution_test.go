package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/clas/nanoflare/internal/nanoflare"
)

func TestRuntimeExecutionControlAndResourceAuthority(t *testing.T) {
	store := nanoflare.NewStore()
	service := nanoflare.NewService(store, discardWriter{})
	app, err := service.CreateApp(nanoflare.CreateAppInput{Name: "Execution", Hostname: "execution.example.com"})
	if err != nil {
		t.Fatal(err)
	}
	kv, err := service.CreateKVNamespace(nanoflare.CreateKVNamespaceInput{Name: "first"})
	if err != nil {
		t.Fatal(err)
	}
	other, err := service.CreateKVNamespace(nanoflare.CreateKVNamespaceInput{Name: "second"})
	if err != nil {
		t.Fatal(err)
	}
	handler := NewRuntimeExecutionServer(service, "runner-secret")
	t.Cleanup(func() { _ = handler.Close() })
	call := func(operation, token string, body any, want int) map[string]string {
		t.Helper()
		encoded, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		r := httptest.NewRequest(http.MethodPost, "/internal/runtime/executions/"+operation, bytes.NewReader(encoded))
		r.Header.Set("X-Nanoflare-Runner-Token", token)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != want {
			t.Fatalf("%s: %d want %d: %s", operation, w.Code, want, w.Body.String())
		}
		result := make(map[string]string)
		if want == http.StatusOK {
			if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
				t.Fatal(err)
			}
		}
		return result
	}
	active := nanoflare.ActiveDeployment{App: app, Deployment: nanoflare.Deployment{ID: "old", AppID: app.ID, KVNamespaces: []nanoflare.KVBinding{{Binding: "KV", ID: kv.ID}}}}
	register := map[string]any{"active": active, "secret_values": map[string]string{"SECRET": "old"}, "compile_options": "options", "object_key": "deployments/old/bundle.json"}
	for _, token := range []string{"", app.RuntimeToken, "wrong-runner"} {
		call("register", token, register, http.StatusUnauthorized)
	}
	session := call("open-session", "runner-secret", map[string]string{}, http.StatusOK)["session_id"]
	register["session_id"], register["registration_id"] = session, "old-registration"
	registered := call("register", "runner-secret", register, http.StatusOK)
	id := registered["execution_id"]
	call("admit", "runner-secret", map[string]string{"execution_id": id, "session_id": session}, http.StatusConflict)
	call("publish", "runner-secret", map[string]string{"execution_id": id, "session_id": session}, http.StatusOK)
	expected := active
	expected.App.SecretValues = map[string]string{"SECRET": "old"}
	expected.Deployment.ObjectKey = "deployments/old/bundle.json"
	expectedSnapshot, err := nanoflare.FreezeExecution(expected, "options")
	if err != nil || expectedSnapshot.ID() != registered["snapshot_id"] {
		t.Fatal("registration lost JSON-hidden snapshot state", err)
	}
	admission := call("admit", "runner-secret", map[string]string{"execution_id": id, "session_id": session}, http.StatusOK)
	pin := call("pin", "runner-secret", map[string]string{"token": admission["token"]}, http.StatusOK)["pin_id"]
	call("retire", "runner-secret", map[string]string{"execution_id": id, "session_id": session}, http.StatusOK)
	call("admit", "runner-secret", map[string]string{"execution_id": id, "session_id": session}, http.StatusConflict)
	call("release", "runner-secret", map[string]string{"pin_id": admission["pin_id"]}, http.StatusOK)
	call("release", "runner-secret", map[string]string{"pin_id": admission["pin_id"]}, http.StatusOK)
	if err := service.KVPut(admission["token"], kv.ID, "hello", []byte("old execution")); err != nil {
		t.Fatal("retired admitted execution denied:", err)
	}
	if _, _, err := service.KVGet(admission["token"], other.ID, "hello"); !errors.Is(err, nanoflare.ErrInvalidCapability) {
		t.Fatal("undeclared namespace accepted:", err)
	}
	if err := service.KVPut(admission["token"], other.ID, "hello", nil); !errors.Is(err, nanoflare.ErrInvalidCapability) {
		t.Fatal("undeclared namespace write accepted:", err)
	}
	if _, err := service.KVListPage(admission["token"], other.ID, nanoflare.RuntimeKVListOptions{}); !errors.Is(err, nanoflare.ErrInvalidCapability) {
		t.Fatal("undeclared namespace list accepted:", err)
	}
	if err := service.KVDelete(admission["token"], other.ID, "hello"); !errors.Is(err, nanoflare.ErrInvalidCapability) {
		t.Fatal("undeclared namespace delete accepted:", err)
	}
	if _, err := service.DBExecute(admission["token"], "undeclared", nanoflare.DBQueryRequest{}); !errors.Is(err, nanoflare.ErrInvalidCapability) {
		t.Fatal("undeclared database accepted:", err)
	}
	if _, _, err := service.ObjectGet(admission["token"], "undeclared", "hello"); !errors.Is(err, nanoflare.ErrInvalidCapability) {
		t.Fatal("undeclared object bucket accepted:", err)
	}
	if _, err := service.AssetFetch(admission["token"], "new", "/index.html"); !errors.Is(err, nanoflare.ErrInvalidCapability) {
		t.Fatal("cross revision assets accepted:", err)
	}
	call("release", "runner-secret", map[string]string{"pin_id": pin}, http.StatusOK)
	call("pin", "runner-secret", map[string]string{"token": admission["token"]}, http.StatusForbidden)
	if _, _, err := service.KVGet(admission["token"], kv.ID, "hello"); !errors.Is(err, nanoflare.ErrInvalidCapability) {
		t.Fatal("released authority accepted:", err)
	}
	if len(handler.pins) != 0 {
		t.Fatal("released pins retained")
	}
	// Registration cannot turn a forged app organization into resource authority.
	active.App.OrgID = "forged"
	call("register", "runner-secret", map[string]any{"active": active, "session_id": session, "registration_id": "forged"}, http.StatusForbidden)
}

func TestRuntimeExecutionDisabledWithoutControlCredential(t *testing.T) {
	handler := NewRuntimeExecutionServer(nanoflare.NewService(nanoflare.NewStore(), discardWriter{}), "")
	t.Cleanup(func() { _ = handler.Close() })
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/internal/runtime/executions/admit", bytes.NewBufferString(`{}`)))
	if w.Code != http.StatusUnauthorized {
		t.Fatal("empty control credential enabled handler")
	}
}

func TestRuntimeExecutionRegistrationSupportsLargeSnapshots(t *testing.T) {
	store := nanoflare.NewStore()
	if err := store.CreateApp(nanoflare.App{ID: "large"}); err != nil {
		t.Fatal(err)
	}
	service := nanoflare.NewService(store, discardWriter{})
	handler := NewRuntimeExecutionServer(service, "runner")
	t.Cleanup(func() { _ = handler.Close() })
	active := nanoflare.ActiveDeployment{App: nanoflare.App{ID: "large"}, Deployment: nanoflare.Deployment{ID: "large-deployment", AppID: "large", Files: []nanoflare.WorkerFile{{Name: "index.js", Content: strings.Repeat("x", 11<<20)}}}}
	open := httptest.NewRequest(http.MethodPost, "/internal/runtime/executions/open-session", strings.NewReader("{}"))
	open.Header.Set("X-Nanoflare-Runner-Token", "runner")
	opened := httptest.NewRecorder()
	handler.ServeHTTP(opened, open)
	var session map[string]string
	if err := json.Unmarshal(opened.Body.Bytes(), &session); err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(map[string]any{"active": active, "session_id": session["session_id"], "registration_id": "large"})
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodPost, "/internal/runtime/executions/register", bytes.NewReader(body))
	r.Header.Set("X-Nanoflare-Runner-Token", "runner")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("snapshot larger than public API body limit rejected: %d %s", w.Code, w.Body.String())
	}
}

func TestRuntimeExecutionRunnerSessionLifetime(t *testing.T) {
	service := nanoflare.NewService(nanoflare.NewStore(), discardWriter{})
	snapshot, err := nanoflare.FreezeExecution(nanoflare.ActiveDeployment{App: nanoflare.App{ID: "a"}, Deployment: nanoflare.Deployment{ID: "d", AppID: "a", KVNamespaces: []nanoflare.KVBinding{{ID: "kv"}}}})
	if err != nil {
		t.Fatal(err)
	}

	handler := NewRuntimeExecutionServer(service, "runner")
	t.Cleanup(func() { _ = handler.Close() })
	call := func(operation string, body map[string]string, want int) map[string]string {
		t.Helper()
		encoded, _ := json.Marshal(body)
		r := httptest.NewRequest(http.MethodPost, "/internal/runtime/executions/"+operation, bytes.NewReader(encoded))
		r.Header.Set("X-Nanoflare-Runner-Token", "runner")
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != want {
			t.Fatalf("%s: %d want %d: %s", operation, w.Code, want, w.Body.String())
		}
		result := make(map[string]string)
		if want == http.StatusOK {
			if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
				t.Fatal(err)
			}
		}
		return result
	}
	call("admit", map[string]string{"execution_id": snapshot.ID()}, http.StatusForbidden)
	session := call("open-session", map[string]string{}, http.StatusOK)["session_id"]
	executionID, err := service.Executions().PrepareResourceGrant(snapshot, session, "test-registration")
	if err != nil {
		t.Fatal(err)
	}
	if err := service.Executions().PublishResourceGrant(session, executionID); err != nil {
		t.Fatal(err)
	}
	admission := call("admit", map[string]string{"execution_id": executionID, "session_id": session}, http.StatusOK)
	call("pin", map[string]string{"token": admission["token"]}, http.StatusOK)
	// The heartbeat renews a still-live session, so sweeping its former deadline
	// must retain both the invocation and its background pin.
	handler.mu.Lock()
	oldDeadline := time.Now().Add(5 * time.Second)
	handler.sessions[session].expires = oldDeadline
	handler.mu.Unlock()
	call("heartbeat", map[string]string{"session_id": session}, http.StatusOK)
	handler.expireSessions(oldDeadline)
	if _, err := service.Executions().Authorize(admission["token"], nanoflare.ExecutionKV, "kv"); err != nil {
		t.Fatal("heartbeat did not retain authority", err)
	}
	service.Executions().Retire(executionID)
	handler.expireSessions(time.Now().Add(2 * runtimeExecutionSessionTTL))
	if _, err := service.Executions().Authorize(admission["token"], nanoflare.ExecutionKV, "kv"); !errors.Is(err, nanoflare.ErrInvalidCapability) {
		t.Fatal("dead runner retained authority", err)
	}
	if service.Executions().ReferencesDeployment("a", "d") {
		t.Fatal("expired retired execution retained")
	}
	if len(handler.pins) != 0 || len(handler.owners) != 0 || len(handler.sessions) != 0 {
		t.Fatal("expired session retained maps")
	}
	call("heartbeat", map[string]string{"session_id": session}, http.StatusForbidden)
	call("pin", map[string]string{"token": admission["token"]}, http.StatusForbidden)
	call("close-session", map[string]string{"session_id": session}, http.StatusOK)
	call("release", map[string]string{"pin_id": admission["pin_id"]}, http.StatusOK)
	// An orderly runner shutdown revokes pins immediately, and listener shutdown
	// also cleans sessions that never reached their orderly close request.

	for _, closeListener := range []bool{false, true} {
		id := call("open-session", map[string]string{}, http.StatusOK)["session_id"]
		executionID, err := service.Executions().PrepareResourceGrant(snapshot, id, "test-registration")
		if err != nil {
			t.Fatal(err)
		}
		if err := service.Executions().PublishResourceGrant(id, executionID); err != nil {
			t.Fatal(err)
		}
		lease := call("admit", map[string]string{"execution_id": executionID, "session_id": id}, http.StatusOK)
		if closeListener {
			if err := handler.Close(); err != nil {
				t.Fatal(err)
			}
		} else {
			call("close-session", map[string]string{"session_id": id}, http.StatusOK)
			call("close-session", map[string]string{"session_id": id}, http.StatusOK)
		}
		if _, err := service.Executions().Authorize(lease["token"], nanoflare.ExecutionKV, "kv"); !errors.Is(err, nanoflare.ErrInvalidCapability) {
			t.Fatal("closed session retained authority", err)
		}
	}
	select {
	case <-handler.done:
	default:
		t.Fatal("listener janitor did not stop")
	}
}

func TestRuntimeExecutionAdmissionSequenceIsIdempotentAndCancellationSafe(t *testing.T) {
	service := nanoflare.NewService(nanoflare.NewStore(), discardWriter{})
	snapshot, err := nanoflare.FreezeExecution(nanoflare.ActiveDeployment{App: nanoflare.App{ID: "app"}, Deployment: nanoflare.Deployment{ID: "rev", AppID: "app"}})
	if err != nil {
		t.Fatal(err)
	}

	handler := NewRuntimeExecutionServer(service, "runner")
	t.Cleanup(func() { _ = handler.Close() })
	call := func(operation string, body map[string]string, want int) map[string]string {
		t.Helper()
		encoded, _ := json.Marshal(body)
		r := httptest.NewRequest(http.MethodPost, "/internal/runtime/executions/"+operation, bytes.NewReader(encoded))
		r.Header.Set("X-Nanoflare-Runner-Token", "runner")
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != want {
			t.Fatalf("%s: %d want %d: %s", operation, w.Code, want, w.Body.String())
		}
		result := map[string]string{}
		if want == 200 {
			if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
				t.Fatal(err)
			}
		}
		return result
	}
	session := call("open-session", map[string]string{}, 200)["session_id"]
	executionID, err := service.Executions().PrepareResourceGrant(snapshot, session, "test-registration")
	if err != nil {
		t.Fatal(err)
	}
	if err := service.Executions().PublishResourceGrant(session, executionID); err != nil {
		t.Fatal(err)
	}
	admit := func(sequence string, want int) map[string]string {
		return call("admit", map[string]string{"session_id": session, "admission_id": sequence, "execution_id": executionID}, want)
	}
	first := admit("1", 200)
	duplicate := admit("1", 200)
	if first["token"] != duplicate["token"] || first["pin_id"] != session+":1" || duplicate["pin_id"] != first["pin_id"] {
		t.Fatal("admission retry minted a new capability")
	}
	if len(handler.pins) != 1 {
		t.Fatal("admission retry duplicated pin")
	}
	call("admit", map[string]string{"session_id": session, "admission_id": "1", "execution_id": "another-revision"}, 403)
	for range 2 {
		call("release", map[string]string{"session_id": session, "pin_id": session + ":1"}, 200)
	}
	admit("1", 403)
	// A canceled HTTP control request can finish out of order. The caller knows
	// this pin ID before admission, so a cancellation arriving first is final.
	call("release", map[string]string{"session_id": session, "pin_id": session + ":2"}, 200)
	admit("2", 403)
	fourth := admit("4", 200)
	admit("3", 403)
	if retry := admit("4", 200); retry["token"] != fourth["token"] {
		t.Fatal("existing admission did not survive watermark advance")
	}
	for _, invalid := range []string{"0", "-1", "random", "18446744073709551616"} {
		admit(invalid, 400)
	}
	other := call("open-session", map[string]string{}, 200)["session_id"]
	call("release", map[string]string{"session_id": other, "pin_id": fourth["pin_id"]}, 403)
	call("admit", map[string]string{"session_id": other, "admission_id": "4", "execution_id": executionID}, 409)
	otherExecution, err := service.Executions().PrepareResourceGrant(snapshot, other, "test-registration")
	if err != nil {
		t.Fatal(err)
	}
	if err := service.Executions().PublishResourceGrant(other, otherExecution); err != nil {
		t.Fatal(err)
	}
	call("admit", map[string]string{"session_id": other, "admission_id": "5", "execution_id": otherExecution}, 200)
	if len(handler.pins) != 2 {
		t.Fatal("session-scoped sequences collided")
	}
	call("release", map[string]string{"session_id": session, "pin_id": fourth["pin_id"]}, 200)
	admit("4", 403)
	if handler.sessions[session].highWater != 4 {
		t.Fatal("admission watermark did not retain cancellation")
	}
}
