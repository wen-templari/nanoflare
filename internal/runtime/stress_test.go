package runtime

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func benchmarkSharedManager(b *testing.B) *Manager {
	b.Helper()
	dir := b.TempDir()
	manager := NewManager(&fakeWriter{}, &fakeLauncher{healthy: true}, dir, filepath.Join(dir, "workerd.capnp"), testSocketDir(b), time.Second, time.Second)
	if err := manager.Write(deployments(0)); err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { manager.Close() })
	return manager
}

func BenchmarkManagerEnsure(b *testing.B) {
	manager := benchmarkSharedManager(b)
	active := deployments(0)[0]
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		ensured, err := manager.Ensure(context.Background(), active)
		if err != nil {
			b.Fatal(err)
		}
		ensured.Release()
	}
}

func BenchmarkManagerEnsureParallel(b *testing.B) {
	manager := benchmarkSharedManager(b)
	active := deployments(0)[0]
	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			ensured, err := manager.Ensure(context.Background(), active)
			if err != nil {
				b.Error(err)
				return
			}
			ensured.Release()
		}
	})
}
