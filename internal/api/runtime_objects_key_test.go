package api

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRuntimeObjectKeyDecodesExactlyOnce(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /objects/{key...}", func(w http.ResponseWriter, r *http.Request) {
		key, err := runtimeObjectKey(r)
		if err != nil {
			http.Error(w, "denied", 400)
			return
		}
		w.Write([]byte(key))
	})
	for _, test := range []struct {
		url, key string
		status   int
	}{
		{"/objects/100%25", "100%", 200},
		{"/objects/a%252Fb", "a%2Fb", 200},
		{"/objects/a%2Fb", "a/b", 200},
		{"/objects/%252e%252e%252fvictim", "%2e%2e%2fvictim", 200},
		{"/objects/a%2F%2e%2e%2Fvictim", "", 400},
	} {
		response := httptest.NewRecorder()
		mux.ServeHTTP(response, httptest.NewRequest("GET", test.url, nil))
		if response.Code != test.status || test.status == 200 && response.Body.String() != test.key {
			t.Errorf("%s: %d %q", test.url, response.Code, response.Body.String())
		}
	}
}
