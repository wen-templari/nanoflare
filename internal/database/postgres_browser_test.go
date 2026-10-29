package database

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"
)

func TestPostgresBrowserRecordTransactionsAndCleanup(t *testing.T) {
	dsn := os.Getenv("NANOFLARE_TEST_POSTGRES_URL")
	if dsn == "" {
		t.Skip("NANOFLARE_TEST_POSTGRES_URL is not configured")
	}
	ctx := context.Background()
	first, err := Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	second, err := Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	key := fmt.Sprintf("browser-test-%d", time.Now().UnixNano())
	defer first.db.Exec(`DELETE FROM oidc_browser_records WHERE id=$1`, key)
	update := func(data []byte) ([]byte, time.Time, error) {
		return []byte("original"), time.Now().Add(time.Hour), nil
	}
	if err := first.UpdateBrowserRecord(ctx, key, update); err != nil {
		t.Fatal(err)
	}
	rollback := errors.New("rollback")
	if err := second.UpdateBrowserRecord(ctx, key, func([]byte) ([]byte, time.Time, error) {
		return []byte("replacement"), time.Now().Add(time.Hour), rollback
	}); !errors.Is(err, rollback) {
		t.Fatal(err)
	}
	entered, release, finished := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	go func() {
		finished <- first.UpdateBrowserRecord(ctx, key, func(data []byte) ([]byte, time.Time, error) {
			close(entered)
			<-release
			return data, time.Now().Add(time.Hour), nil
		})
	}()
	<-entered
	lockedCtx, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
	err = second.UpdateBrowserRecord(lockedCtx, key, func([]byte) ([]byte, time.Time, error) {
		t.Error("concurrent update entered while locked")
		return nil, time.Time{}, nil
	})
	cancel()
	close(release)
	if err == nil {
		t.Error("blocked transaction did not honor deadline")
	}
	if err := <-finished; err != nil {
		t.Fatal(err)
	}
	if err := second.UpdateBrowserRecord(ctx, key, func(data []byte) ([]byte, time.Time, error) {
		if string(data) != "original" {
			t.Errorf("rollback changed payload: %q", data)
		}
		return data, time.Now().Add(-time.Minute), nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := first.CleanupBrowserRecords(ctx); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := first.db.QueryRow(`SELECT count(*) FROM oidc_browser_records WHERE id=$1`, key).Scan(&count); err != nil || count != 0 {
		t.Fatalf("cleanup count=%d error=%v", count, err)
	}
}
