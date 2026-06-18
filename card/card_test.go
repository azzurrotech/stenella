package card

import (
	"encoding/xml"
	"strings"
	"testing"
)

func TestNewCard(t *testing.T) {
	c := &Card{
		ID:      "test_1",
		Title:   "Test Title",
		Summary: "Test Summary",
		Detail:  "Test Detail",
		Created: "2026-01-01T00:00:00Z",
	}

	if c.ID != "test_1" {
		t.Errorf("ID = %q, want %q", c.ID, "test_1")
	}
	if c.Title != "Test Title" {
		t.Errorf("Title = %q, want %q", c.Title, "Test Title")
	}
	if c.Summary != "Test Summary" {
		t.Errorf("Summary = %q, want %q", c.Summary, "Test Summary")
	}
	if c.Detail != "Test Detail" {
		t.Errorf("Detail = %q, want %q", c.Detail, "Test Detail")
	}
	if c.ParentID != "" {
		t.Errorf("ParentID should be empty, got %q", c.ParentID)
	}
	if len(c.Children) != 0 {
		t.Errorf("Children should be empty, got %d", len(c.Children))
	}
}

func TestCardParentChild(t *testing.T) {
	parent := &Card{
		ID:    "parent_1",
		Title: "Parent",
	}
	child := &Card{
		ID:       "child_1",
		Title:    "Child",
		ParentID: "parent_1",
	}

	parent.Children = append(parent.Children, child)

	if len(parent.Children) != 1 {
		t.Fatalf("len(parent.Children) = %d, want 1", len(parent.Children))
	}
	if parent.Children[0].ID != "child_1" {
		t.Errorf("child ID = %q, want %q", parent.Children[0].ID, "child_1")
	}
	if parent.Children[0].ParentID != "parent_1" {
		t.Errorf("child ParentID = %q, want %q", parent.Children[0].ParentID, "parent_1")
	}
}

func TestDeck(t *testing.T) {
	deck := &Deck{
		Cards: []*Card{
			{ID: "c1", Title: "Card 1"},
			{ID: "c2", Title: "Card 2"},
		},
	}

	if len(deck.Cards) != 2 {
		t.Fatalf("len(deck.Cards) = %d, want 2", len(deck.Cards))
	}
	if deck.Cards[0].Title != "Card 1" {
		t.Errorf("deck.Cards[0].Title = %q, want %q", deck.Cards[0].Title, "Card 1")
	}
	if deck.Cards[1].ID != "c2" {
		t.Errorf("deck.Cards[1].ID = %q, want %q", deck.Cards[1].ID, "c2")
	}
}

func TestCardXMLMarshal(t *testing.T) {
	c := &Card{
		ID:      "xml_test",
		Title:   "XML Card",
		Summary: "A summary",
		Detail:  "Some details",
		Created: "2026-06-18T12:00:00Z",
	}

	data, err := xml.Marshal(c)
	if err != nil {
		t.Fatalf("xml.Marshal failed: %v", err)
	}

	output := string(data)
	if !strings.Contains(output, "<id>xml_test</id>") {
		t.Errorf("XML missing id element: %s", output)
	}
	if !strings.Contains(output, "<title>XML Card</title>") {
		t.Errorf("XML missing title: %s", output)
	}
	if !strings.Contains(output, "<summary>A summary</summary>") {
		t.Errorf("XML missing summary: %s", output)
	}
	if !strings.Contains(output, "<detail>Some details</detail>") {
		t.Errorf("XML missing detail: %s", output)
	}
}

func TestCardXMLUnmarshal(t *testing.T) {
	xmlData := `<card>
		<id>um_test</id>
		<title>Unmarshal Card</title>
		<summary>A summary</summary>
		<detail>Some details</detail>
		<parent_id>parent_1</parent_id>
		<created>2026-06-18T12:00:00Z</created>
	</card>`

	var c Card
	if err := xml.Unmarshal([]byte(xmlData), &c); err != nil {
		t.Fatalf("xml.Unmarshal failed: %v", err)
	}

	if c.ID != "um_test" {
		t.Errorf("ID = %q, want %q", c.ID, "um_test")
	}
	if c.Title != "Unmarshal Card" {
		t.Errorf("Title = %q, want %q", c.Title, "Unmarshal Card")
	}
	if c.Summary != "A summary" {
		t.Errorf("Summary = %q, want %q", c.Summary, "A summary")
	}
	if c.Detail != "Some details" {
		t.Errorf("Detail = %q, want %q", c.Detail, "Some details")
	}
	if c.ParentID != "parent_1" {
		t.Errorf("ParentID = %q, want %q", c.ParentID, "parent_1")
	}
}

func TestDeckXMLRoundTrip(t *testing.T) {
	original := &Deck{
		Cards: []*Card{
			{ID: "a1", Title: "Alpha", Summary: "First card", Detail: "Details A", Created: "now"},
			{ID: "b2", Title: "Beta", Summary: "Second card", Detail: "Details B", ParentID: "a1", Created: "then"},
		},
	}

	data, err := xml.MarshalIndent(original, "", "  ")
	if err != nil {
		t.Fatalf("xml.MarshalIndent failed: %v", err)
	}

	var decoded Deck
	if err := xml.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("xml.Unmarshal failed: %v", err)
	}

	if len(decoded.Cards) != 2 {
		t.Fatalf("len(decoded.Cards) = %d, want 2", len(decoded.Cards))
	}
	if decoded.Cards[0].ID != "a1" {
		t.Errorf("decoded.Cards[0].ID = %q, want %q", decoded.Cards[0].ID, "a1")
	}
	if decoded.Cards[1].ParentID != "a1" {
		t.Errorf("decoded.Cards[1].ParentID = %q, want %q", decoded.Cards[1].ParentID, "a1")
	}
	if decoded.Cards[1].Title != "Beta" {
		t.Errorf("decoded.Cards[1].Title = %q, want %q", decoded.Cards[1].Title, "Beta")
	}
}

func TestCardWithChildrenXML(t *testing.T) {
	child := &Card{ID: "child1", Title: "Child Card", Summary: "child", Detail: "child detail", Created: "now"}
	parent := &Card{
		ID:       "parent1",
		Title:    "Parent Card",
		Summary:  "parent",
		Detail:   "parent detail",
		Children: []*Card{child},
		Created:  "now",
	}

	data, err := xml.MarshalIndent(parent, "", "  ")
	if err != nil {
		t.Fatalf("xml.MarshalIndent failed: %v", err)
	}

	output := string(data)
	if !strings.Contains(output, "<children>") {
		t.Errorf("XML missing <children> tag: %s", output)
	}
	if !strings.Contains(output, "<id>child1</id>") {
		t.Errorf("XML missing child card id: %s", output)
	}
}

func TestEmptyDeck(t *testing.T) {
	deck := &Deck{}
	if deck.Cards == nil {
		t.Log("Empty deck has nil Cards (expected before init)")
	}
	data, _ := xml.Marshal(deck)
	if !strings.Contains(string(data), "<deck>") {
		t.Errorf("Empty deck XML missing root: %s", string(data))
	}
}

func TestCardPointerSemantics(t *testing.T) {
	c1 := &Card{ID: "ptr1", Title: "Original"}
	c2 := c1
	c2.Title = "Modified"

	if c1.Title != "Modified" {
		t.Error("Card pointer should share underlying data")
	}
}
