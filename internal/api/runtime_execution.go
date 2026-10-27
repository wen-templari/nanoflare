package api

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/clas/nanoflare/internal/nanoflare"
)

// RuntimeExecutionServer is mounted only on the private runtime listener.
// Its global control credential is never a Worker binding or app credential.
type RuntimeExecutionServer struct {
	service   *nanoflare.Service
	token     string
	mu        sync.Mutex
	pins      map[string]runtimeExecutionPin
	sessions  map[string]*runtimeExecutionSession
	owners    map[string]string // resource capability token -> owning runner session
	stop      chan struct{}
	done      chan struct{}
	closeOnce sync.Once
	closed    bool
	output    RuntimeLogAppender
}

type runtimeExecutionPin struct {
	lease       *nanoflare.ExecutionLease
	sessionID   string
	executionID string
}
type runtimeExecutionSession struct {
	expires   time.Time
	pins      map[string]struct{}
	tokens    map[string]int
	highWater uint64
}

const runtimeExecutionSessionTTL = time.Minute

func NewRuntimeExecutionServer(service *nanoflare.Service, runnerControlToken string, outputs ...RuntimeLogAppender) *RuntimeExecutionServer {
	s := &RuntimeExecutionServer{service: service, token: runnerControlToken,
		pins: make(map[string]runtimeExecutionPin), sessions: make(map[string]*runtimeExecutionSession), owners: make(map[string]string),
		stop: make(chan struct{}), done: make(chan struct{})}
	if len(outputs) > 0 {
		s.output = outputs[0]
	}
	go func() {
		defer close(s.done)
		ticker := time.NewTicker(15 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case now := <-ticker.C:
				s.expireSessions(now)
			case <-s.stop:
				return
			}
		}
	}()
	return s
}

// Close stops the idle-session janitor and revokes this listener's admissions.
func (s *RuntimeExecutionServer) Close() error {
	s.closeOnce.Do(func() {
		close(s.stop)
		<-s.done
		s.mu.Lock()
		defer s.mu.Unlock()
		s.closed = true
		for id := range s.sessions {
			s.closeSessionLocked(id)
		}
	})
	return nil
}

func (s *RuntimeExecutionServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if s.token == "" || subtle.ConstantTimeCompare([]byte(r.Header.Get("X-Nanoflare-Runner-Token")), []byte(s.token)) != 1 {
		writeError(w, http.StatusUnauthorized, errors.New("invalid runner credential"))
		return
	}
	s.expireSessions(time.Now())
	s.mu.Lock()
	closed := s.closed
	s.mu.Unlock()
	if closed {
		writeError(w, http.StatusServiceUnavailable, errors.New("execution control is closed"))
		return
	}
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		writeError(w, http.StatusMethodNotAllowed, errors.New("unsupported execution operation"))
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 64<<20)
	defer r.Body.Close()
	var request struct {
		Active         nanoflare.ActiveDeployment `json:"active"`
		Secrets        map[string]string          `json:"secret_values"`
		CompileOptions string                     `json:"compile_options"`
		ObjectKey      string                     `json:"object_key"`
		ExecutionID    string                     `json:"execution_id"`
		Token          string                     `json:"token"`
		PinID          string                     `json:"pin_id"`
		AdmissionID    string                     `json:"admission_id"`
		RegistrationID string                     `json:"registration_id"`
		SessionID      string                     `json:"session_id"`
		Message        string                     `json:"message"`
	}
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	switch strings.TrimPrefix(r.URL.Path, "/internal/runtime/executions/") {
	case "diagnostic":
		line, accepted, err := s.service.ProcessExecutionDiagnostic(r.Context(), request.SessionID, request.ExecutionID, request.Message)
		if err != nil {
			writeError(w, http.StatusForbidden, err)
			return
		}
		if accepted {
			appendRuntimeLog(s.output, line)
		}
		w.WriteHeader(http.StatusNoContent)
	case "open-session":
		id, err := runtimeExecutionRandomID()
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		s.mu.Lock()
		if s.closed {
			s.mu.Unlock()
			writeError(w, http.StatusServiceUnavailable, errors.New("execution control is closed"))
			return
		}
		s.sessions[id] = &runtimeExecutionSession{expires: time.Now().Add(runtimeExecutionSessionTTL), pins: make(map[string]struct{}), tokens: make(map[string]int)}
		s.service.Executions().RenewResourceSession(id, s.sessions[id].expires)
		s.mu.Unlock()
		writeJSON(w, http.StatusOK, map[string]string{"session_id": id})
	case "heartbeat":
		s.mu.Lock()
		session := s.liveSessionLocked(request.SessionID, time.Now())
		if session != nil {
			session.expires = time.Now().Add(runtimeExecutionSessionTTL)
			s.service.Executions().RenewResourceSession(request.SessionID, session.expires)
		}
		s.mu.Unlock()
		if session == nil {
			writeError(w, http.StatusForbidden, errors.New("runner session expired"))
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{})
	case "close-session":
		s.mu.Lock()
		s.closeSessionLocked(request.SessionID)
		s.mu.Unlock()
		writeJSON(w, http.StatusOK, map[string]string{})
	case "register":
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.liveSessionLocked(request.SessionID, time.Now()) == nil || s.closed {
			writeError(w, http.StatusForbidden, errors.New("runner session expired"))
			return
		}
		request.Active.App.SecretValues = request.Secrets
		request.Active.Deployment.ObjectKey = request.ObjectKey
		id, snapshotID, err := s.service.PrepareExecutionGrant(request.Active, request.CompileOptions, request.SessionID, request.RegistrationID)
		if err != nil {
			writeError(w, http.StatusForbidden, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"execution_id": id, "snapshot_id": snapshotID})
	case "publish":
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.liveSessionLocked(request.SessionID, time.Now()) == nil || s.closed {
			writeError(w, http.StatusForbidden, errors.New("runner session expired"))
			return
		}
		if err := s.service.Executions().PublishResourceGrant(request.SessionID, request.ExecutionID); err != nil {
			writeError(w, http.StatusConflict, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{})
	case "admit":
		id := request.AdmissionID
		var sequence uint64
		if id == "" {
			var err error
			id, err = runtimeExecutionRandomID()
			if err != nil {
				writeError(w, http.StatusInternalServerError, err)
				return
			}
		} else {
			var err error
			sequence, err = strconv.ParseUint(id, 10, 64)
			if err != nil || sequence == 0 {
				writeError(w, http.StatusBadRequest, errors.New("admission id must be a positive sequence"))
				return
			}
			id = request.SessionID + ":" + strconv.FormatUint(sequence, 10)
		}
		if len(id) > 256 {
			writeError(w, http.StatusBadRequest, errors.New("admission id is too long"))
			return
		}
		token, err := s.admit(request.SessionID, id, request.ExecutionID, sequence)
		if err != nil {
			status := http.StatusForbidden
			if errors.Is(err, nanoflare.ErrExecutionRetired) {
				status = http.StatusConflict
			}
			writeError(w, status, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"token": token, "pin_id": id})
	case "pin":
		s.mu.Lock()
		owner := s.owners[request.Token]
		live := s.liveSessionLocked(owner, time.Now()) != nil
		s.mu.Unlock()
		if !live {
			writeError(w, http.StatusForbidden, errors.New("runner session expired"))
			return
		}
		pin, err := s.service.Executions().Pin(request.Token)
		if err != nil {
			writeError(w, http.StatusForbidden, err)
			return
		}
		id, err := s.keepPin(pin, owner)
		if err != nil {
			pin.Release()
			writeError(w, http.StatusForbidden, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"pin_id": id})
	case "release":
		if len(request.PinID) > 256 {
			writeError(w, http.StatusBadRequest, errors.New("pin id is too long"))
			return
		}
		s.mu.Lock()
		owner := request.SessionID
		if pin, ok := s.pins[request.PinID]; ok {
			if owner != "" && owner != pin.sessionID {
				s.mu.Unlock()
				writeError(w, http.StatusForbidden, errors.New("pin belongs to another runner session"))
				return
			}
			owner = pin.sessionID
		}
		if session := s.liveSessionLocked(owner, time.Now()); session != nil && strings.HasPrefix(request.PinID, owner+":") {
			sequence, err := strconv.ParseUint(strings.TrimPrefix(request.PinID, owner+":"), 10, 64)
			if err != nil || sequence == 0 {
				s.mu.Unlock()
				writeError(w, http.StatusBadRequest, errors.New("invalid admission pin id"))
				return
			}
			if sequence > session.highWater {
				session.highWater = sequence
			}
		}
		s.releasePinLocked(request.PinID)
		s.mu.Unlock()
		writeJSON(w, http.StatusOK, map[string]string{})
	case "retire":
		if err := s.service.Executions().RetireResourceGrant(request.SessionID, request.ExecutionID); err != nil {
			writeError(w, http.StatusForbidden, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{})
	default:
		writeError(w, http.StatusNotFound, errors.New("unknown execution operation"))
	}
}

func runtimeExecutionRandomID() (string, error) {
	var entropy [32]byte
	if _, err := rand.Read(entropy[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(entropy[:]), nil
}

func (s *RuntimeExecutionServer) keepPin(lease *nanoflare.ExecutionLease, sessionID string) (string, error) {
	id, err := runtimeExecutionRandomID()
	if err != nil {
		return "", err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	session := s.liveSessionLocked(sessionID, time.Now())
	if session == nil || s.closed {
		return "", errors.New("runner session expired")
	}
	s.storePinLocked(id, lease, sessionID, "")
	return id, nil
}

// A caller-chosen monotonic sequence makes admission cancellation safe even if
// its response is lost. The watermark rejects late requests without tombstones.
// The runner serializes admissions, so unseen lower sequences are always stale.
func (s *RuntimeExecutionServer) admit(sessionID, id, executionID string, sequence uint64) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	session := s.liveSessionLocked(sessionID, time.Now())
	if session == nil || s.closed {
		return "", errors.New("runner session expired")
	}
	if existing, ok := s.pins[id]; ok {
		if existing.sessionID != sessionID || existing.executionID != executionID {
			return "", errors.New("admission id belongs to another execution")
		}
		return existing.lease.Token(), nil
	}
	if sequence != 0 {
		if sequence <= session.highWater {
			return "", errors.New("admission was canceled or superseded")
		}
		session.highWater = sequence
	}
	lease, err := s.service.Executions().AdmitResourceGrant(sessionID, executionID)
	if err != nil {
		return "", err
	}
	s.storePinLocked(id, lease, sessionID, executionID)
	return lease.Token(), nil
}

func (s *RuntimeExecutionServer) storePinLocked(id string, lease *nanoflare.ExecutionLease, sessionID, executionID string) {
	session := s.sessions[sessionID]
	s.pins[id] = runtimeExecutionPin{lease: lease, sessionID: sessionID, executionID: executionID}
	session.pins[id] = struct{}{}
	session.tokens[lease.Token()]++
	s.owners[lease.Token()] = sessionID
}

func (s *RuntimeExecutionServer) liveSessionLocked(id string, now time.Time) *runtimeExecutionSession {
	session := s.sessions[id]
	if session != nil && !session.expires.After(now) {
		s.closeSessionLocked(id)
		return nil
	}
	return session
}

func (s *RuntimeExecutionServer) releasePinLocked(id string) {
	pin, ok := s.pins[id]
	if !ok {
		return
	}
	delete(s.pins, id)
	if session := s.sessions[pin.sessionID]; session != nil {
		delete(session.pins, id)
		session.tokens[pin.lease.Token()]--
		if session.tokens[pin.lease.Token()] == 0 {
			delete(session.tokens, pin.lease.Token())
			delete(s.owners, pin.lease.Token())
		}
	}
	pin.lease.Release()
}

func (s *RuntimeExecutionServer) closeSessionLocked(id string) {
	session := s.sessions[id]
	if session == nil {
		return
	}
	for pinID := range session.pins {
		s.releasePinLocked(pinID)
	}
	delete(s.sessions, id)
	s.service.Executions().RevokeResourceSession(id)
}

func (s *RuntimeExecutionServer) expireSessions(now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, session := range s.sessions {
		if !session.expires.After(now) {
			s.closeSessionLocked(id)
		}
	}
}
