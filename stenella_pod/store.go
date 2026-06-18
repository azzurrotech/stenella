package stenella_pod

import (
	"encoding/xml"
	"fmt"
	"os"
	"sync"
)

type Field struct {
	Name  string `xml:"name,attr"`
	Value string `xml:",chardata"`
}

type Record struct {
	ID     int     `xml:"id"`
	Fields []Field `xml:"field"`
}

type Database struct {
	XMLName xml.Name `xml:"database"`
	Records []Record `xml:"record"`
}

type PODStore struct {
	dataFile string
	mu       sync.RWMutex
	opMu     sync.Mutex
}

func (s *PODStore) LockOp()    { s.opMu.Lock() }
func (s *PODStore) UnlockOp()  { s.opMu.Unlock() }
func (s *PODStore) RLockOp()   { s.opMu.Lock() }
func (s *PODStore) RUnlockOp() { s.opMu.Unlock() }

func NewStore(dataFile string) *PODStore {
	return &PODStore{dataFile: dataFile}
}

func (s *PODStore) LoadDB() (*Database, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var db Database
	if _, err := os.Stat(s.dataFile); os.IsNotExist(err) {
		return &db, nil
	}
	data, err := os.ReadFile(s.dataFile)
	if err != nil {
		return nil, fmt.Errorf("failed to read database file: %w", err)
	}
	if len(data) == 0 {
		return &db, nil
	}
	if err := xml.Unmarshal(data, &db); err != nil {
		return nil, fmt.Errorf("failed to parse XML: %w", err)
	}
	return &db, nil
}

func (s *PODStore) SaveDB(db *Database) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	data, err := xml.MarshalIndent(db, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal XML: %w", err)
	}
	header := []byte(xml.Header)
	fullData := append(header, data...)
	if err := os.WriteFile(s.dataFile, fullData, 0644); err != nil {
		return fmt.Errorf("failed to write database file: %w", err)
	}
	return nil
}

func (s *PODStore) GetNextID(db *Database) int {
	maxID := 0
	for _, record := range db.Records {
		if record.ID > maxID {
			maxID = record.ID
		}
	}
	return maxID + 1
}

type IndexEntry struct {
	Field     string
	Value     string
	RecordIDs []int
}

type IndexMap map[string][]IndexEntry

type PODStoreWithIndex struct {
	*PODStore
	index   IndexMap
	indexMu sync.RWMutex
}

func NewStoreWithIndex(dataFile string) *PODStoreWithIndex {
	baseStore := NewStore(dataFile)
	s := &PODStoreWithIndex{
		PODStore: baseStore,
		index:    make(IndexMap),
	}
	if db, err := s.LoadDB(); err == nil && db != nil {
		s.rebuildIndex(db)
	}
	return s
}

func (s *PODStoreWithIndex) rebuildIndex(db *Database) {
	s.indexMu.Lock()
	defer s.indexMu.Unlock()

	s.index = make(IndexMap)
	for _, record := range db.Records {
		for _, field := range record.Fields {
			key := fmt.Sprintf("%s:%s", field.Name, field.Value)
			existing, found := s.index[key]
			if !found {
				s.index[key] = []IndexEntry{{
					Field: field.Name, Value: field.Value, RecordIDs: []int{record.ID},
				}}
			} else {
				foundID := false
				for i := range existing {
					if existing[i].Field == field.Name && existing[i].Value == field.Value {
						for _, id := range existing[i].RecordIDs {
							if id == record.ID {
								foundID = true
								break
							}
						}
						if !foundID {
							existing[i].RecordIDs = append(existing[i].RecordIDs, record.ID)
						}
						break
					}
				}
				if !foundID {
					s.index[key] = append(s.index[key], IndexEntry{
						Field: field.Name, Value: field.Value, RecordIDs: []int{record.ID},
					})
				}
			}
		}
	}
}

func (s *PODStoreWithIndex) GetIndex(fieldName, fieldValue string) []int {
	s.indexMu.RLock()
	defer s.indexMu.RUnlock()
	key := fmt.Sprintf("%s:%s", fieldName, fieldValue)
	entries := s.index[key]
	if len(entries) == 0 {
		return nil
	}
	return entries[0].RecordIDs
}

func (s *PODStoreWithIndex) UpdateIndexForInsert(record Record) {
	s.indexMu.Lock()
	defer s.indexMu.Unlock()

	for _, field := range record.Fields {
		key := fmt.Sprintf("%s:%s", field.Name, field.Value)
		if _, exists := s.index[key]; !exists {
			s.index[key] = []IndexEntry{{
				Field: field.Name, Value: field.Value, RecordIDs: []int{record.ID},
			}}
		} else {
			for i := range s.index[key] {
				if s.index[key][i].Field == field.Name && s.index[key][i].Value == field.Value {
					found := false
					for _, id := range s.index[key][i].RecordIDs {
						if id == record.ID {
							found = true
							break
						}
					}
					if !found {
						s.index[key][i].RecordIDs = append(s.index[key][i].RecordIDs, record.ID)
					}
					break
				}
			}
		}
	}
}

func (s *PODStoreWithIndex) fastLookup(fieldName, fieldValue string) []int {
	s.indexMu.RLock()
	defer s.indexMu.RUnlock()
	key := fmt.Sprintf("%s:%s", fieldName, fieldValue)
	entries := s.index[key]
	if len(entries) == 0 {
		return nil
	}
	return entries[0].RecordIDs
}

func (s *PODStoreWithIndex) updateIndexForUpdate(record Record, oldFields map[string]string) {
	s.indexMu.Lock()
	defer s.indexMu.Unlock()
	for _, field := range record.Fields {
		oldVal, existed := oldFields[field.Name]
		if existed && oldVal != field.Value {
			oldKey := fmt.Sprintf("%s:%s", field.Name, oldVal)
			if entries, ok := s.index[oldKey]; ok {
				for i := range entries {
					if entries[i].Field == field.Name && entries[i].Value == oldVal {
						newIDs := make([]int, 0, len(entries[i].RecordIDs))
						for _, id := range entries[i].RecordIDs {
							if id != record.ID {
								newIDs = append(newIDs, id)
							}
						}
						entries[i].RecordIDs = newIDs
						break
					}
				}
			}
		}
		newKey := fmt.Sprintf("%s:%s", field.Name, field.Value)
		if _, exists := s.index[newKey]; !exists {
			s.index[newKey] = []IndexEntry{{
				Field: field.Name, Value: field.Value, RecordIDs: []int{record.ID},
			}}
		} else {
			found := false
			for i := range s.index[newKey] {
				if s.index[newKey][i].Field == field.Name && s.index[newKey][i].Value == field.Value {
					for _, id := range s.index[newKey][i].RecordIDs {
						if id == record.ID {
							found = true
							break
						}
					}
					if !found {
						s.index[newKey][i].RecordIDs = append(s.index[newKey][i].RecordIDs, record.ID)
					}
					break
				}
			}
		}
	}
}

func (s *PODStoreWithIndex) updateIndexForDelete(recordID int) {
	s.indexMu.Lock()
	defer s.indexMu.Unlock()
	for key, entries := range s.index {
		for i := range entries {
			newIDs := make([]int, 0, len(entries[i].RecordIDs))
			for _, id := range entries[i].RecordIDs {
				if id != recordID {
					newIDs = append(newIDs, id)
				}
			}
			entries[i].RecordIDs = newIDs
			s.index[key] = entries
			if len(entries[i].RecordIDs) == 0 {
				delete(s.index, key)
			}
		}
	}
}

func (s *PODStore) loadDB() (*Database, error) {
	return s.LoadDB()
}

func (s *PODStore) saveDB(db *Database) error {
	return s.SaveDB(db)
}

func (s *PODStore) getNextID(db *Database) int {
	return s.GetNextID(db)
}

func (s *PODStoreWithIndex) updateIndexForInsert(record Record) {
	s.UpdateIndexForInsert(record)
}

func (s *PODStoreWithIndex) DeleteRecord(id int) error {
	s.LockOp()
	defer s.UnlockOp()

	db, err := s.LoadDB()
	if err != nil {
		return err
	}
	var updated []Record
	for _, r := range db.Records {
		if r.ID != id {
			updated = append(updated, r)
		}
	}
	db.Records = updated
	s.updateIndexForDelete(id)
	return s.SaveDB(db)
}
