package runtime

import (
	"encoding/json"
	"net/http"
	"slices"

	"github.com/clas/nanoflare/internal/nanoflare"
)

const (
	applicationLoaderCallLimit = 32
	applicationLoaderKeyLimit  = 128
)

type applicationLoaderKey struct {
	Binding     string  `json:"binding"`
	ID          *string `json:"id"`
	Child       string  `json:"child"`
	Subrequests *uint32 `json:"subrequests"`
}

func (m *DynamicManager) admitApplicationLoader(w http.ResponseWriter, r *http.Request, key string, snapshot *dynamicSnapshot) {
	if r.Method != http.MethodPost {
		http.Error(w, "unsupported loader operation", http.StatusMethodNotAllowed)
		return
	}
	var input applicationLoaderKey
	err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&input)
	if err != nil || (input.ID != nil && len(*input.ID) > 256) ||
		(input.Child != "" && (len(input.Child) != 36 || input.ID != nil || input.Subrequests == nil)) ||
		(input.Child == "" && input.Subrequests != nil) {
		http.Error(w, "invalid loader key", http.StatusBadRequest)
		return
	}
	if !slices.Contains(snapshot.frozen.WorkerLoaders(), nanoflare.WorkerLoaderBinding{Binding: input.Binding}) {
		http.Error(w, "undeclared loader binding", http.StatusForbidden)
		return
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	invocation := m.invocations[r.Header.Get("X-Nanoflare-Invocation")]
	if invocation == nil || invocation.key != key || !invocation.started || invocation.complete {
		http.Error(w, "invalid invocation", http.StatusForbidden)
		return
	}
	if input.Child != "" {
		if len(invocation.loaderSubrequests) >= applicationLoaderCallLimit {
			http.Error(w, "loader invocation limit exceeded", http.StatusTooManyRequests)
			return
		}
		if invocation.loaderSubrequests == nil {
			invocation.loaderSubrequests = make(map[string]uint32)
		}
		if _, exists := invocation.loaderSubrequests[input.Child]; exists {
			http.Error(w, "loader child already registered", http.StatusConflict)
			return
		}
		invocation.loaderSubrequests[input.Child] = *input.Subrequests
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if invocation.applicationLoaderCalls >= applicationLoaderCallLimit {
		http.Error(w, "loader invocation limit exceeded", http.StatusTooManyRequests)
		return
	}
	if input.ID != nil {
		cacheKey := loaderCacheKey{binding: input.Binding, id: *input.ID}
		if _, exists := snapshot.applicationLoaderKeys[cacheKey]; !exists {
			if len(snapshot.applicationLoaderKeys) >= applicationLoaderKeyLimit {
				http.Error(w, "loader cache key limit exceeded", http.StatusTooManyRequests)
				return
			}
			if snapshot.applicationLoaderKeys == nil {
				snapshot.applicationLoaderKeys = make(map[loaderCacheKey]struct{})
			}
			snapshot.applicationLoaderKeys[cacheKey] = struct{}{}
		}
	}
	invocation.applicationLoaderCalls++
	w.WriteHeader(http.StatusNoContent)
}

type loaderCacheKey struct {
	binding string
	id      string
}
