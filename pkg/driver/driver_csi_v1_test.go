package driver

import (
	"context"
	"testing"
	"time"
)

// TestAcquireAndReleaseVolumeLock ensures a lock can be acquired and released.
func TestAcquireAndReleaseVolumeLock(t *testing.T) {
	d := &CSIDriver{
		volumeLocks:   make(map[string]*keyLock),
		snapshotLocks: make(map[string]*keyLock),
	}

	ctx := context.Background()
	volID := "vol-test"

	unlock, err := d.acquireVolumeLock(ctx, volID)
	if err != nil {
		t.Fatalf("expected lock to succeed, got error: %v", err)
	}
	if unlock == nil {
		t.Fatalf("expected non-nil unlock function")
	}

	unlock()
	_, err = d.acquireVolumeLock(ctx, volID)
	if err != nil {
		t.Fatalf("expected lock to succeed after unlock, got error: %v", err)
	}
}

// TestAcquireVolumeLockTimeout ensures lock acquisition times out correctly.
func TestAcquireVolumeLockTimeout(t *testing.T) {
	d := &CSIDriver{
		volumeLocks:   make(map[string]*keyLock),
		snapshotLocks: make(map[string]*keyLock),
	}

	volID := "vol-timeout"

	// Acquire the lock and don't release
	unlock, err := d.acquireVolumeLock(context.Background(), volID)
	if err != nil {
		t.Fatalf("expected first acquire to succeed, got error: %v", err)
	}
	if unlock == nil {
		t.Fatalf("expected unlock function to be non-nil")
	}

	// Try acquiring again with short timeout
	start := time.Now()
	_, err = d.acquireVolumeLock(context.Background(), volID)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatalf("expected timeout error but got none")
	}
	if elapsed < 250*time.Millisecond {
		t.Fatalf("expected blocking for ~300ms, got only %v", elapsed)
	}
}

// TestSnapshotLock is just to ensure snapshotLocks uses same logic
func TestAcquireSnapshotLock(t *testing.T) {
	d := &CSIDriver{
		volumeLocks:   make(map[string]*keyLock),
		snapshotLocks: make(map[string]*keyLock),
	}

	snapID := "snap-1"
	unlock, err := d.acquireSnapshotLock(context.Background(), snapID)
	if err != nil {
		t.Fatalf("expected snapshot lock to succeed, got error: %v", err)
	}
	if unlock == nil {
		t.Fatalf("expected non-nil unlock function")
	}

	// Release and ensure we can lock again
	unlock()
	_, err = d.acquireSnapshotLock(context.Background(), snapID)
	if err != nil {
		t.Fatalf("expected lock after unlock to succeed, got error: %v", err)
	}
}
