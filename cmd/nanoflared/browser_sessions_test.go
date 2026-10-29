package main

import (
	"github.com/clas/nanoflare/internal/browserstore"
	"strings"
	"testing"
)

func TestBrowserStoreSelectionPreservesKeylessComposeUpgrade(t *testing.T) {
	persistent := &testPersistentBrowserStore{Store: browserstore.NewMemory()}
	store, warning := selectBrowserSessionStore(persistent, "")
	if store.Persistent() || !strings.Contains(warning, "NANOFLARE_SECRET_KEY") || !strings.Contains(warning, "DATABASE_URL") {
		t.Fatal("keyless compose upgrade must retain memory sessions with actionable warning")
	}
	store, warning = selectBrowserSessionStore(persistent, "0123456789abcdef0123456789abcdef")
	if store != persistent || warning != "" {
		t.Fatal("explicit persistence must survive repository wrapping")
	}
}

type testPersistentBrowserStore struct{ browserstore.Store }

func (*testPersistentBrowserStore) Persistent() bool { return true }
