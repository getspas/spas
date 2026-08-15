package lock

import "testing"

func TestAcquirePreventsConcurrentOwner(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	first, err := Acquire(dir, "link")
	if err != nil {
		t.Fatal(err)
	}
	defer first.Release()
	if _, err := Acquire(dir, "link"); err == nil {
		t.Fatal("second Acquire() error = nil, want non-nil")
	}
	if err := first.Release(); err != nil {
		t.Fatal(err)
	}
	second, err := Acquire(dir, "link")
	if err != nil {
		t.Fatal(err)
	}
	if err := second.Release(); err != nil {
		t.Fatal(err)
	}
}
