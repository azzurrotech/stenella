package storage

import (
	"os"
	"path/filepath"
	"testing"

	"stenella/card"
)

func TestNewStore(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test_deck.xml")
	s := New(path)

	if s == nil {
		t.Fatal("New returned nil")
	}
	if s.Deck == nil {
		t.Fatal("Deck is nil")
	}
	if len(s.Deck.Cards) != 0 {
		t.Errorf("Expected empty deck, got %d cards", len(s.Deck.Cards))
	}
}

func TestAddAndGetAll(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "deck.xml")
	s := New(path)

	c1 := &card.Card{ID: "c1", Title: "Card One"}
	c2 := &card.Card{ID: "c2", Title: "Card Two"}

	s.Add(c1)
	s.Add(c2)

	all := s.GetAll()
	if len(all) != 2 {
		t.Fatalf("len(GetAll()) = %d, want 2", len(all))
	}
	if all[0].ID != "c1" {
		t.Errorf("all[0].ID = %q, want %q", all[0].ID, "c1")
	}
	if all[1].Title != "Card Two" {
		t.Errorf("all[1].Title = %q, want %q", all[1].Title, "Card Two")
	}
}

func TestGetChildren(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "deck.xml")
	s := New(path)

	parent := &card.Card{ID: "parent", Title: "Parent"}
	child1 := &card.Card{ID: "child1", Title: "Child 1", ParentID: "parent"}
	child2 := &card.Card{ID: "child2", Title: "Child 2", ParentID: "parent"}
	orphan := &card.Card{ID: "orphan", Title: "Orphan"}

	s.Add(parent)
	s.Add(child1)
	s.Add(child2)
	s.Add(orphan)

	children := s.GetChildren("parent")
	if len(children) != 2 {
		t.Fatalf("len(GetChildren('parent')) = %d, want 2", len(children))
	}

	ids := map[string]bool{}
	for _, c := range children {
		ids[c.ID] = true
	}
	if !ids["child1"] {
		t.Error("child1 not in parent's children")
	}
	if !ids["child2"] {
		t.Error("child2 not in parent's children")
	}

	orphanChildren := s.GetChildren("nonexistent")
	if len(orphanChildren) != 0 {
		t.Errorf("GetChildren('nonexistent') = %d, want 0", len(orphanChildren))
	}

	noChildren := s.GetChildren("orphan")
	if len(noChildren) != 0 {
		t.Errorf("GetChildren('orphan') = %d, want 0", len(noChildren))
	}
}

func TestSaveAndLoad(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "deck.xml")

	s1 := New(path)
	s1.Add(&card.Card{ID: "save1", Title: "Save Test", Summary: "s", Detail: "d", Created: "now"})
	s1.Add(&card.Card{ID: "save2", Title: "Another", ParentID: "save1", Created: "then"})
	s1.Save()

	if _, err := os.Stat(path); os.IsNotExist(err) {
		t.Fatalf("Save did not create file: %s", path)
	}

	s2 := New(path)
	all := s2.GetAll()
	if len(all) != 2 {
		t.Fatalf("after Load: len(GetAll()) = %d, want 2", len(all))
	}
	if all[0].ID != "save1" {
		t.Errorf("all[0].ID = %q, want %q", all[0].ID, "save1")
	}
	if all[0].Title != "Save Test" {
		t.Errorf("all[0].Title = %q, want %q", all[0].Title, "Save Test")
	}
	if all[1].ParentID != "save1" {
		t.Errorf("all[1].ParentID = %q, want %q", all[1].ParentID, "save1")
	}

	children := s2.GetChildren("save1")
	if len(children) != 1 {
		t.Fatalf("after Load: len(GetChildren('save1')) = %d, want 1", len(children))
	}
	if children[0].ID != "save2" {
		t.Errorf("child ID = %q, want %q", children[0].ID, "save2")
	}
}

func TestLoadNonExistentFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nonexistent.xml")

	s := New(path)
	if s.Deck == nil {
		t.Fatal("Deck is nil after loading nonexistent file")
	}
	if len(s.Deck.Cards) != 0 {
		t.Errorf("Expected empty deck, got %d cards", len(s.Deck.Cards))
	}
}

func TestMultipleAdds(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "deck.xml")
	s := New(path)

	n := 100
	for i := 0; i < n; i++ {
		s.Add(&card.Card{
			ID:    string(rune('A' + i)),
			Title: "Card",
		})
	}

	all := s.GetAll()
	if len(all) != n {
		t.Errorf("len(GetAll()) = %d, want %d", len(all), n)
	}
}

func TestSaveCreatesDirectory(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sub", "nested", "deck.xml")

	s := New(path)
	s.Add(&card.Card{ID: "mkdir_test", Title: "Dir Create Test"})
	s.Save()

	if _, err := os.Stat(path); os.IsNotExist(err) {
		t.Fatalf("Save did not create nested directory structure: %s", path)
	}
}
