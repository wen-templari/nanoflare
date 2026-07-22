package runtime

import (
	"context"
	"net"
	"testing"
	"time"
)

func BenchmarkLazyManagerEnsureCold(b *testing.B) {
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		launcher := &fakeLauncher{healthy: true}
		manager := NewLazyManager(&fakeWriter{}, launcher, b.TempDir(), "127.0.0.1", availablePortForBenchmark(b), time.Second, time.Second, time.Minute)
		ensured, err := manager.Ensure(context.Background(), deployments(0)[0])
		if err != nil {
			b.Fatal(err)
		}
		ensured.Release()
		if err := manager.Close(); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkLazyManagerEnsureWarm(b *testing.B) {
	manager := NewLazyManager(&fakeWriter{}, &fakeLauncher{healthy: true}, b.TempDir(), "127.0.0.1", availablePortForBenchmark(b), time.Second, time.Second, time.Minute)
	defer manager.Close()
	active := deployments(0)[0]
	ensured, err := manager.Ensure(context.Background(), active)
	if err != nil {
		b.Fatal(err)
	}
	ensured.Release()

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		ensured, err := manager.Ensure(context.Background(), active)
		if err != nil {
			b.Fatal(err)
		}
		ensured.Release()
	}
}

func BenchmarkLazyManagerEnsureWarmParallel(b *testing.B) {
	manager := NewLazyManager(&fakeWriter{}, &fakeLauncher{healthy: true}, b.TempDir(), "127.0.0.1", availablePortForBenchmark(b), time.Second, time.Second, time.Minute)
	defer manager.Close()
	active := deployments(0)[0]
	ensured, err := manager.Ensure(context.Background(), active)
	if err != nil {
		b.Fatal(err)
	}
	ensured.Release()

	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			ensured, err := manager.Ensure(context.Background(), active)
			if err != nil {
				panic(err)
			}
			ensured.Release()
		}
	})
}

func availablePortForBenchmark(b *testing.B) int {
	b.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		b.Fatal(err)
	}
	defer listener.Close()
	return listener.Addr().(*net.TCPAddr).Port
}
