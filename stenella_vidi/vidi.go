package stenella_vidi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

type Backend interface {
	Store(data map[string]interface{}) error
	Query(params map[string]interface{}) ([]map[string]interface{}, error)
	Get(id string) (map[string]interface{}, error)
	Delete(id string) error
}

type MemoryBackend struct {
	data map[string]map[string]interface{}
	mu   sync.RWMutex
}

func NewMemoryBackend() *MemoryBackend {
	return &MemoryBackend{data: make(map[string]map[string]interface{})}
}

func (m *MemoryBackend) Store(d map[string]interface{}) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	id, ok := d["id"].(string)
	if !ok || id == "" {
		return fmt.Errorf("memory backend requires an 'id' field")
	}
	m.data[id] = d
	return nil
}

func (m *MemoryBackend) Query(params map[string]interface{}) ([]map[string]interface{}, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var results []map[string]interface{}
	for _, record := range m.data {
		if matchesQuery(record, params) {
			copy := make(map[string]interface{})
			for k, v := range record {
				copy[k] = v
			}
			results = append(results, copy)
		}
	}
	return results, nil
}

func (m *MemoryBackend) Get(id string) (map[string]interface{}, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	record, ok := m.data[id]
	if !ok {
		return nil, fmt.Errorf("record not found: %s", id)
	}
	copy := make(map[string]interface{})
	for k, v := range record {
		copy[k] = v
	}
	return copy, nil
}

func (m *MemoryBackend) Delete(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.data[id]; !ok {
		return fmt.Errorf("record not found: %s", id)
	}
	delete(m.data, id)
	return nil
}

type FilesystemBackend struct {
	basePath string
	mu       sync.RWMutex
}

func NewFilesystemBackend(basePath string) *FilesystemBackend {
	if err := os.MkdirAll(basePath, 0755); err != nil {
		panic(fmt.Sprintf("failed to create storage directory: %v", err))
	}
	return &FilesystemBackend{basePath: basePath}
}

func (fs *FilesystemBackend) Store(data map[string]interface{}) error {
	fs.mu.Lock()
	defer fs.mu.Unlock()

	record := make(map[string]interface{})
	for k, v := range data {
		record[k] = v
	}

	id, ok := record["id"].(string)
	if !ok || id == "" {
		id = fmt.Sprintf("%d", time.Now().UnixNano())
		record["id"] = id
	}

	filename := filepath.Join(fs.basePath, id+".json")
	jsonData, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return err
	}

	tmpFile := filename + ".tmp"
	if err := os.WriteFile(tmpFile, jsonData, 0644); err != nil {
		return err
	}
	if err := os.Rename(tmpFile, filename); err != nil {
		os.Remove(tmpFile)
		return err
	}
	return nil
}

func (fs *FilesystemBackend) Query(params map[string]interface{}) ([]map[string]interface{}, error) {
	fs.mu.RLock()
	defer fs.mu.RUnlock()

	var results []map[string]interface{}

	if id, ok := params["id"].(string); ok && id != "" {
		rec, err := fs.getUnlocked(id)
		if err == nil && matchesQuery(rec, params) {
			results = append(results, rec)
		}
		return results, nil
	}

	files, err := os.ReadDir(fs.basePath)
	if err != nil {
		return nil, err
	}

	for _, f := range files {
		if !strings.HasSuffix(f.Name(), ".json") {
			continue
		}
		id := strings.TrimSuffix(f.Name(), ".json")
		rec, err := fs.getUnlocked(id)
		if err != nil {
			continue
		}
		if matchesQuery(rec, params) {
			results = append(results, rec)
		}
	}
	return results, nil
}

func (fs *FilesystemBackend) Get(id string) (map[string]interface{}, error) {
	fs.mu.RLock()
	defer fs.mu.RUnlock()
	return fs.getUnlocked(id)
}

func (fs *FilesystemBackend) getUnlocked(id string) (map[string]interface{}, error) {
	filename := filepath.Join(fs.basePath, id+".json")
	data, err := os.ReadFile(filename)
	if err != nil {
		return nil, err
	}
	var record map[string]interface{}
	if err := json.Unmarshal(data, &record); err != nil {
		return nil, err
	}
	return record, nil
}

func (fs *FilesystemBackend) Delete(id string) error {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	filename := filepath.Join(fs.basePath, id+".json")
	if err := os.Remove(filename); err != nil {
		return err
	}
	return nil
}

func matchesQuery(record, params map[string]interface{}) bool {
	if len(params) == 0 {
		return true
	}
	for k, v := range params {
		if recVal, ok := record[k]; !ok || recVal != v {
			return false
		}
	}
	return true
}

type Config struct {
	Storage        Backend
	Format         string
	AutoGenerateID bool
	TableName      string
	Endpoint       string
}

func DefaultConfig() Config {
	return Config{
		Format: "json", AutoGenerateID: true, TableName: "form_data", Endpoint: "/forms",
	}
}

type VIDI struct {
	config  Config
	storage Backend
}

func New(cfg Config) *VIDI {
	if cfg.Storage == nil {
		cfg.Storage = NewMemoryBackend()
	}
	if cfg.Format == "" {
		cfg.Format = "json"
	}
	if cfg.TableName == "" {
		cfg.TableName = "form_data"
	}
	if cfg.Endpoint == "" {
		cfg.Endpoint = "/forms"
	}
	return &VIDI{config: cfg, storage: cfg.Storage}
}

func (v *VIDI) Handle(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !v.isIntercepted(r) {
			next.ServeHTTP(w, r)
			return
		}
		switch r.Method {
		case http.MethodPost:
			v.handlePost(w, r)
		case http.MethodGet:
			v.handleGet(w, r)
		default:
			next.ServeHTTP(w, r)
		}
	})
}

func (v *VIDI) isIntercepted(r *http.Request) bool {
	return r.URL.Path == v.config.Endpoint || strings.HasPrefix(r.URL.Path, v.config.Endpoint+"/")
}

func (v *VIDI) handlePost(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Bad Request", http.StatusBadRequest)
		return
	}
	data := make(map[string]interface{})
	for k, vals := range r.Form {
		if len(vals) == 1 {
			data[k] = vals[0]
		} else {
			data[k] = vals
		}
	}
	if v.config.AutoGenerateID {
		if _, ok := data["id"]; !ok {
			data["id"] = fmt.Sprintf("%d", time.Now().UnixNano())
		}
		if _, ok := data["created_at"]; !ok {
			data["created_at"] = time.Now().UTC().Format(time.RFC3339)
		}
	}
	if err := v.storage.Store(data); err != nil {
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}
	v.sendResponse(w, data, http.StatusCreated)
}

func (v *VIDI) handleGet(w http.ResponseWriter, r *http.Request) {
	params := make(map[string]interface{})
	for k, vals := range r.URL.Query() {
		if len(vals) == 1 {
			params[k] = vals[0]
		} else {
			params[k] = vals
		}
	}
	results, err := v.storage.Query(params)
	if err != nil {
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}
	v.sendResponse(w, results, http.StatusOK)
}

func (v *VIDI) sendResponse(w http.ResponseWriter, data interface{}, status int) {
	var resp []byte
	var ctype string
	var err error

	switch v.config.Format {
	case "xml":
		resp, err = ToXML(data)
		ctype = "application/xml"
	case "sql":
		if m, ok := data.(map[string]interface{}); ok {
			resp, err = ToSQL(m, v.config.TableName)
			ctype = "text/plain"
		} else {
			resp, err = ToJSON(data)
			ctype = "application/json"
		}
	default:
		resp, err = ToJSON(data)
		ctype = "application/json"
	}
	if err != nil {
		http.Error(w, "Format conversion error", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", ctype)
	w.WriteHeader(status)
	w.Write(resp)
}
