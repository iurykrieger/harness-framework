package registry_test

import (
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/iurykrieger/harness-framework/lib/registry"
)

func TestWithFileLock_Serializes(t *testing.T) {
	dir := t.TempDir()
	lockPath := filepath.Join(dir, "test.lock")

	var mu sync.Mutex
	var order []int
	var wg sync.WaitGroup

	work := func(id int, holdMS int) {
		defer wg.Done()
		err := registry.WithFileLock(lockPath, func() error {
			mu.Lock()
			order = append(order, id)
			mu.Unlock()
			time.Sleep(time.Duration(holdMS) * time.Millisecond)
			return nil
		})
		if err != nil {
			t.Errorf("WithFileLock #%d: %v", id, err)
		}
	}

	wg.Add(2)
	go work(1, 50)
	time.Sleep(5 * time.Millisecond) // ensure goroutine 1 grabs first
	go work(2, 0)
	wg.Wait()

	if len(order) != 2 || order[0] != 1 || order[1] != 2 {
		t.Fatalf("expected [1,2], got %v", order)
	}
}

func TestWithFileLock_PropagatesError(t *testing.T) {
	dir := t.TempDir()
	lockPath := filepath.Join(dir, "test.lock")

	want := errSentinel{}
	got := registry.WithFileLock(lockPath, func() error { return want })
	if got != want {
		t.Fatalf("got %v, want %v", got, want)
	}
}

type errSentinel struct{}

func (errSentinel) Error() string { return "sentinel" }
