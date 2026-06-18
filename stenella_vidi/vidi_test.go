package stenella_vidi

import (
	"testing"
)

func TestNewMemoryBackend(t *testing.T) {
	m := NewMemoryBackend()
	if m == nil {
		t.Fatal("NewMemoryBackend returned nil")
	}
	if m.data == nil {
		t.Fatal("MemoryBackend.data is nil")
	}
	if len(m.data) != 0 {
		t.Errorf("Expected empty data, got %d records", len(m.data))
	}
}

func TestMemoryBackendStoreAndGet(t *testing.T) {
	m := NewMemoryBackend()

	record := map[string]interface{}{
		"id":   "test_1",
		"name": "Test Record",
		"kind": "Event",
	}

	if err := m.Store(record); err != nil {
		t.Fatalf("Store failed: %v", err)
	}

	got, err := m.Get("test_1")
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}
	if got["name"] != "Test Record" {
		t.Errorf("name = %v, want %q", got["name"], "Test Record")
	}
	if got["kind"] != "Event" {
		t.Errorf("kind = %v, want %q", got["kind"], "Event")
	}
}

func TestMemoryBackendStoreNoID(t *testing.T) {
	m := NewMemoryBackend()

	record := map[string]interface{}{
		"name": "No ID",
	}
	if err := m.Store(record); err == nil {
		t.Error("Expected error for record without id field")
	}
}

func TestMemoryBackendGetNotFound(t *testing.T) {
	m := NewMemoryBackend()
	_, err := m.Get("nonexistent")
	if err == nil {
		t.Error("Expected error for nonexistent record")
	}
}

func TestMemoryBackendDelete(t *testing.T) {
	m := NewMemoryBackend()

	m.Store(map[string]interface{}{"id": "del_1", "value": "to delete"})

	if err := m.Delete("del_1"); err != nil {
		t.Fatalf("Delete failed: %v", err)
	}

	_, err := m.Get("del_1")
	if err == nil {
		t.Error("Expected error after deleting record")
	}
}

func TestMemoryBackendDeleteNotFound(t *testing.T) {
	m := NewMemoryBackend()
	if err := m.Delete("nonexistent"); err == nil {
		t.Error("Expected error deleting nonexistent record")
	}
}

func TestMemoryBackendQueryAll(t *testing.T) {
	m := NewMemoryBackend()
	m.Store(map[string]interface{}{"id": "a", "kind": "Event"})
	m.Store(map[string]interface{}{"id": "b", "kind": "Faction"})
	m.Store(map[string]interface{}{"id": "c", "kind": "Event"})

	results, err := m.Query(map[string]interface{}{})
	if err != nil {
		t.Fatalf("Query failed: %v", err)
	}
	if len(results) != 3 {
		t.Errorf("len(Query(all)) = %d, want 3", len(results))
	}
}

func TestMemoryBackendQueryFilter(t *testing.T) {
	m := NewMemoryBackend()
	m.Store(map[string]interface{}{"id": "a", "kind": "Event", "name": "Alpha"})
	m.Store(map[string]interface{}{"id": "b", "kind": "Faction", "name": "Beta"})
	m.Store(map[string]interface{}{"id": "c", "kind": "Event", "name": "Gamma"})

	results, err := m.Query(map[string]interface{}{"kind": "Event"})
	if err != nil {
		t.Fatalf("Query failed: %v", err)
	}
	if len(results) != 2 {
		t.Errorf("len(Query(kind=Event)) = %d, want 2", len(results))
	}

	results, err = m.Query(map[string]interface{}{"kind": "Leader"})
	if err != nil {
		t.Fatalf("Query failed: %v", err)
	}
	if len(results) != 0 {
		t.Errorf("len(Query(kind=Leader)) = %d, want 0", len(results))
	}
}

func TestMemoryBackendQueryByID(t *testing.T) {
	m := NewMemoryBackend()
	m.Store(map[string]interface{}{"id": "target", "name": "Target", "kind": "Event"})
	m.Store(map[string]interface{}{"id": "other", "name": "Other", "kind": "Faction"})

	results, err := m.Query(map[string]interface{}{"id": "target"})
	if err != nil {
		t.Fatalf("Query by id failed: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("len(Query(id=target)) = %d, want 1", len(results))
	}
	if results[0]["name"] != "Target" {
		t.Errorf("name = %v, want %q", results[0]["name"], "Target")
	}
}

func TestMemoryBackendStoreOverwrite(t *testing.T) {
	m := NewMemoryBackend()

	m.Store(map[string]interface{}{"id": "same", "value": "original"})
	m.Store(map[string]interface{}{"id": "same", "value": "updated"})

	got, _ := m.Get("same")
	if got["value"] != "updated" {
		t.Errorf("value = %v, want %q", got["value"], "updated")
	}
}

func TestMemoryBackendQueryReturnsCopy(t *testing.T) {
	m := NewMemoryBackend()
	m.Store(map[string]interface{}{"id": "cp", "value": "original"})

	results, _ := m.Query(map[string]interface{}{"id": "cp"})
	results[0]["value"] = "modified"

	original, _ := m.Get("cp")
	if original["value"] != "original" {
		t.Error("Query should return copies that don't affect stored data")
	}
}
