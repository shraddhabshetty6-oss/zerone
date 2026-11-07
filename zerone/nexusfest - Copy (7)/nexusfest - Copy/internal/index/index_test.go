package index_test

import (
	"os"
	"path/filepath"
	"testing"

	"studentdb/internal/index"
)

func TestIndexInsertLookupDelete(t *testing.T) {
	idx := index.New()

	// Insert items
	keys := []uint32{100, 50, 150, 25, 75, 125, 175, 10, 30}
	for _, k := range keys {
		slot := k * 2
		if err := idx.Insert(k, slot); err != nil {
			t.Fatalf("Insert(%d) failed: %v", k, err)
		}
	}

	// Duplicate rejection
	if err := idx.Insert(100, 999); err == nil {
		t.Fatal("Insert duplicate ID should return error")
	}

	// Lookup
	for _, k := range keys {
		slot, found := idx.Lookup(k)
		if !found || slot != k*2 {
			t.Fatalf("Lookup(%d) = (%d, %v); want (%d, true)", k, slot, found, k*2)
		}
	}

	// Range query
	rangeEntries := idx.Range(25, 100, 10)
	if len(rangeEntries) != 5 { // 25, 30, 50, 75, 100
		t.Fatalf("Range [25, 100] returned %d entries; want 5", len(rangeEntries))
	}
	for i := 1; i < len(rangeEntries); i++ {
		if rangeEntries[i].StudentID <= rangeEntries[i-1].StudentID {
			t.Fatal("Range entries are not strictly ascending")
		}
	}

	// Delete
	if !idx.Delete(50) {
		t.Fatal("Delete(50) returned false")
	}
	if _, found := idx.Lookup(50); found {
		t.Fatal("Lookup(50) found after delete")
	}
	if idx.Delete(50) {
		t.Fatal("Second Delete(50) should return false")
	}
}

func TestSnapshotSaveAndLoad(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "index_snap_test")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	snapPath := filepath.Join(tmpDir, "students.idx")

	idx := index.New()
	for i := uint32(1); i <= 100; i++ {
		_ = idx.Insert(i*10, i)
	}

	checkpointLSN := uint64(555)
	slotCount := uint64(100)

	if err := idx.SaveSnapshot(snapPath, checkpointLSN, slotCount); err != nil {
		t.Fatalf("SaveSnapshot failed: %v", err)
	}

	loadedIdx := index.New()
	gotLSN, gotSlots, err := loadedIdx.LoadSnapshot(snapPath)
	if err != nil {
		t.Fatalf("LoadSnapshot failed: %v", err)
	}

	if gotLSN != checkpointLSN || gotSlots != slotCount {
		t.Fatalf("Loaded metadata mismatch: LSN=%d (want %d), slots=%d (want %d)", gotLSN, checkpointLSN, gotSlots, slotCount)
	}

	if loadedIdx.Len() != 100 {
		t.Fatalf("Loaded index len = %d; want 100", loadedIdx.Len())
	}

	for i := uint32(1); i <= 100; i++ {
		slot, found := loadedIdx.Lookup(i * 10)
		if !found || slot != i {
			t.Fatalf("Loaded lookup(%d) = (%d, %v); want (%d, true)", i*10, slot, found, i)
		}
	}
}

