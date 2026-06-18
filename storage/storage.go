package storage

import (
	"encoding/xml"
	"os"
	"path/filepath"
	"sync"

	"stenella/card"
)

type Store struct {
	mu     sync.RWMutex
	file   string
	Deck   *card.Deck
}

func New(path string) *Store {
	s := &Store{file: path, Deck: &card.Deck{}}
	os.MkdirAll(filepath.Dir(path), 0755)
	s.Load()
	return s
}

func (s *Store) Load() {
	s.mu.Lock()
	defer s.mu.Unlock()
	data, err := os.ReadFile(s.file)
	if err != nil {
		s.Deck = &card.Deck{}
		return
	}
	xml.Unmarshal(data, s.Deck)
	if s.Deck.Cards == nil {
		s.Deck.Cards = []*card.Card{}
	}
}

func (s *Store) Save() {
	s.mu.Lock()
	defer s.mu.Unlock()
	data, _ := xml.MarshalIndent(s.Deck, "", "  ")
	os.WriteFile(s.file, data, 0644)
}

func (s *Store) GetAll() []*card.Card {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.Deck.Cards
}

func (s *Store) Add(c *card.Card) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Deck.Cards = append(s.Deck.Cards, c)
}

func (s *Store) GetChildren(parentID string) []*card.Card {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var children []*card.Card
	for _, c := range s.Deck.Cards {
		if c.ParentID == parentID {
			children = append(children, c)
		}
	}
	return children
}
