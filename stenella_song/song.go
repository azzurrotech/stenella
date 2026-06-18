package stenella_song

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

type Config struct {
	Port        int
	PublicDir   string
	HandlersDir string
}

func DefaultConfig() Config {
	port := 8080
	if p := os.Getenv("SONG_PORT"); p != "" {
		fmt.Sscanf(p, "%d", &port)
	}
	return Config{
		Port:        port,
		PublicDir:   "./public",
		HandlersDir: "./handlers/post",
	}
}

type Executor struct {
	handlersDir string
	timeout     time.Duration
}

func NewExecutor(handlersDir string) *Executor {
	return &Executor{
		handlersDir: handlersDir,
		timeout:     30 * time.Second,
	}
}

func (e *Executor) SetTimeout(d time.Duration) { e.timeout = d }

func (e *Executor) Execute(handlerName string, r *http.Request, pathParams map[string]string) ([]byte, error) {
	handlerPath := filepath.Join(e.handlersDir, handlerName+".go")
	if _, err := os.Stat(handlerPath); os.IsNotExist(err) {
		return nil, fmt.Errorf("handler file not found: %s", handlerPath)
	}

	env := os.Environ()
	for k, v := range pathParams {
		env = append(env, fmt.Sprintf("SONG_PATH_%s=%s", strings.ToUpper(k), v))
	}
	query := r.URL.Query()
	for k, vals := range query {
		env = append(env, fmt.Sprintf("SONG_QUERY_%s=%s", strings.ToUpper(k), strings.Join(vals, ",")))
	}
	if r.Method == http.MethodPost {
		r.ParseForm()
		for k, vals := range r.Form {
			env = append(env, fmt.Sprintf("SONG_FORM_%s=%s", strings.ToUpper(k), strings.Join(vals, ",")))
		}
	}
	bodyBytes, _ := io.ReadAll(r.Body)
	if len(bodyBytes) > 0 {
		r.Body = io.NopCloser(bytes.NewBuffer(bodyBytes))
		env = append(env, fmt.Sprintf("SONG_RAW_BODY=%s", string(bodyBytes)))
	}

	secrets := GetSecrets()
	for k, v := range secrets {
		env = append(env, fmt.Sprintf("SONG_SECRET_%s=%s", strings.ToUpper(k), v))
	}

	cmd := exec.Command("go", "run", handlerPath)
	cmd.Env = env

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	ctx, cancel := context.WithTimeout(context.Background(), e.timeout)
	defer cancel()

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("failed to start handler: %w", err)
	}

	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	select {
	case <-ctx.Done():
		cmd.Process.Kill()
		<-done
		return nil, fmt.Errorf("handler timed out after %v", e.timeout)
	case err := <-done:
		if err != nil {
			log.Printf("Handler %s failed: %v\n%s", handlerName, err, stderr.String())
			return nil, fmt.Errorf("handler execution failed: %w", err)
		}
	}

	output := stdout.Bytes()
	if len(output) > 0 {
		var js json.RawMessage
		if err := json.Unmarshal(output, &js); err != nil {
			log.Printf("Handler %s output is not valid JSON", handlerName)
		}
	}
	log.Printf("Handler %s completed", handlerName)
	return output, nil
}

type Router struct {
	handlersDir string
	routes      []Route
}

type Route struct {
	Pattern     *regexp.Regexp
	HandlerName string
	ParamNames  []string
	FilePath    string
}

func NewRouter(handlersDir string) *Router {
	r := &Router{handlersDir: handlersDir}
	r.scanHandlers()
	return r
}

func (r *Router) scanHandlers() {
	if _, err := os.Stat(r.handlersDir); os.IsNotExist(err) {
		os.MkdirAll(r.handlersDir, 0755)
		return
	}
	filepath.Walk(r.handlersDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() || !strings.HasSuffix(path, ".go") {
			return nil
		}
		relPath, _ := filepath.Rel(r.handlersDir, path)
		pattern := "/" + strings.TrimSuffix(relPath, ".go")
		pattern = strings.ReplaceAll(pattern, "\\", "/")
		regexPattern := convertToRegex(pattern)
		paramNames := extractParamNames(pattern)
		re, err := regexp.Compile("^" + regexPattern + "$")
		if err != nil {
			return nil
		}
		r.routes = append(r.routes, Route{
			Pattern:     re,
			HandlerName: strings.TrimSuffix(filepath.Base(path), ".go"),
			ParamNames:  paramNames,
			FilePath:    path,
		})
		return nil
	})
}

func convertToRegex(pattern string) string {
	escaped := regexp.QuoteMeta(pattern)
	re := regexp.MustCompile(`\\\{(.*?)\\\}`)
	return re.ReplaceAllString(escaped, `([^/]+)`)
}

func extractParamNames(pattern string) []string {
	re := regexp.MustCompile(`\{(.*?)\}`)
	matches := re.FindAllStringSubmatch(pattern, -1)
	names := make([]string, len(matches))
	for i, match := range matches {
		if len(match) > 1 {
			names[i] = match[1]
		}
	}
	return names
}

func (r *Router) Match(path string) (string, map[string]string, error) {
	if len(r.routes) == 0 {
		return "", nil, fmt.Errorf("no routes registered")
	}
	sort.Slice(r.routes, func(i, j int) bool {
		return len(r.routes[i].Pattern.String()) > len(r.routes[j].Pattern.String())
	})
	params := make(map[string]string)
	for _, route := range r.routes {
		matches := route.Pattern.FindStringSubmatch(path)
		if matches == nil {
			continue
		}
		for i, name := range route.ParamNames {
			if i+1 < len(matches) {
				params[name] = matches[i+1]
			}
		}
		return route.HandlerName, params, nil
	}
	return "", nil, fmt.Errorf("no matching route found for %s", path)
}

func (r *Router) GetRoutes() []Route { return r.routes }

type SecretsManager struct {
	secrets map[string]string
	mu      sync.RWMutex
	loaded  bool
}

var globalSecrets *SecretsManager

func InitSecrets() error {
	sm := &SecretsManager{secrets: make(map[string]string)}
	sm.loadFromEnv()
	secretsFile := os.Getenv("SONG_SECRETS_FILE")
	if secretsFile != "" {
		if err := sm.loadFromFile(secretsFile); err != nil {
			return fmt.Errorf("failed to load secrets file: %w", err)
		}
	}
	sm.loaded = true
	globalSecrets = sm
	log.Printf("Secrets initialized: %d secret(s) loaded", len(sm.secrets))
	return nil
}

func (sm *SecretsManager) loadFromEnv() {
	const prefix = "SONG_SECRET_"
	for _, envVar := range os.Environ() {
		parts := strings.SplitN(envVar, "=", 2)
		if len(parts) != 2 {
			continue
		}
		if strings.HasPrefix(parts[0], prefix) {
			sm.secrets[strings.TrimPrefix(parts[0], prefix)] = parts[1]
		}
	}
}

func (sm *SecretsManager) loadFromFile(path string) error {
	file, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("cannot open secrets file %s: %w", path, err)
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	lineNum := 0
	for scanner.Scan() {
		lineNum++
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.SplitN(line, "=", 2)
		if len(parts) != 2 {
			log.Printf("Skipping invalid line %d in secrets file", lineNum)
			continue
		}
		sm.secrets[strings.TrimSpace(parts[0])] = strings.Trim(strings.TrimSpace(parts[1]), `"'`)
	}
	return scanner.Err()
}

func GetSecret(name string) (string, bool) {
	if globalSecrets == nil {
		return "", false
	}
	globalSecrets.mu.RLock()
	defer globalSecrets.mu.RUnlock()
	val, ok := globalSecrets.secrets[strings.ToUpper(name)]
	return val, ok
}

func GetSecretOrDefault(name, defaultVal string) string {
	if val, ok := GetSecret(name); ok {
		return val
	}
	return defaultVal
}

func RequireSecret(name string) string {
	val, ok := GetSecret(name)
	if !ok {
		panic(fmt.Sprintf("required secret %s is not set", name))
	}
	return val
}

func ReloadSecrets() error {
	return InitSecrets()
}

func SecretCount() int {
	if globalSecrets == nil {
		return 0
	}
	globalSecrets.mu.RLock()
	defer globalSecrets.mu.RUnlock()
	return len(globalSecrets.secrets)
}

func GetSecrets() map[string]string {
	if globalSecrets == nil {
		return map[string]string{}
	}
	globalSecrets.mu.RLock()
	defer globalSecrets.mu.RUnlock()
	copy := make(map[string]string, len(globalSecrets.secrets))
	for k, v := range globalSecrets.secrets {
		copy[k] = v
	}
	return copy
}

type APISpec struct {
	Name      string         `json:"name"`
	Version   string         `json:"version"`
	Generated string         `json:"generated"`
	Endpoints []EndpointSpec `json:"endpoints"`
}

type EndpointSpec struct {
	Path        string          `json:"path"`
	Method      string          `json:"method"`
	Handler     string          `json:"handler"`
	Description string          `json:"description,omitempty"`
	Parameters  []ParameterSpec `json:"parameters,omitempty"`
}

type ParameterSpec struct {
	Name string `json:"name"`
	In   string `json:"in"`
}

type SpecGenerator struct {
	handlersDir string
	lastScan    time.Time
	cachedSpec  []byte
}

func NewSpecGenerator(handlersDir string) *SpecGenerator {
	return &SpecGenerator{handlersDir: handlersDir}
}

func (sg *SpecGenerator) Generate() ([]byte, error) {
	spec := APISpec{
		Name: "SONG API", Version: "1.0.0",
		Generated: time.Now().UTC().Format(time.RFC3339),
	}
	if _, err := os.Stat(sg.handlersDir); os.IsNotExist(err) {
		data, _ := json.MarshalIndent(spec, "", "  ")
		sg.cachedSpec = data
		return sg.cachedSpec, nil
	}
	filepath.Walk(sg.handlersDir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || !strings.HasSuffix(path, ".go") {
			return err
		}
		relPath, _ := filepath.Rel(sg.handlersDir, path)
		urlPath := "/" + strings.TrimSuffix(relPath, ".go")
		urlPath = strings.ReplaceAll(urlPath, "\\", "/")
		handlerName := strings.TrimSuffix(filepath.Base(relPath), ".go")
		spec.Endpoints = append(spec.Endpoints, EndpointSpec{
			Path: urlPath, Method: "POST", Handler: handlerName,
			Description: fmt.Sprintf("Handler: %s", handlerName),
		})
		return nil
	})
	output, err := json.MarshalIndent(spec, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("failed to marshal spec: %w", err)
	}
	sg.cachedSpec = output
	sg.lastScan = time.Now()
	log.Printf("API spec generated: %d endpoint(s)", len(spec.Endpoints))
	return output, nil
}

func (sg *SpecGenerator) GetCachedSpec() []byte { return sg.cachedSpec }

func (sg *SpecGenerator) Refresh() {
	sg.cachedSpec = nil
	if _, err := sg.Generate(); err != nil {
		log.Printf("Spec refresh failed: %v", err)
	}
}

type Server struct {
	config   Config
	router   *Router
	executor *Executor
	specGen  *SpecGenerator
}

func NewServer(cfg Config) *Server {
	return &Server{
		config:   cfg,
		router:   NewRouter(cfg.HandlersDir),
		executor: NewExecutor(cfg.HandlersDir),
		specGen:  NewSpecGenerator(cfg.HandlersDir),
	}
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	log.Printf("%s %s", r.Method, r.URL.Path)

	switch {
	case r.URL.Path == "/health":
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"status":"healthy","service":"song"}`))
		return
	case r.URL.Path == "/api/spec":
		spec, err := s.specGen.Generate()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write(spec)
		return
	}

	switch r.Method {
	case http.MethodGet, http.MethodHead:
		fs := http.FileServer(http.Dir(s.config.PublicDir))
		fs.ServeHTTP(w, r)
	case http.MethodPost:
		s.handleAPIRequest(w, r)
	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) handleAPIRequest(w http.ResponseWriter, r *http.Request) {
	handlerName, params, err := s.router.Match(r.URL.Path)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}

	go func() {
		result, execErr := s.executor.Execute(handlerName, r, params)
		if execErr != nil {
			log.Printf("Handler %s failed: %v", handlerName, execErr)
			return
		}
		log.Printf("Handler %s completed: %s", handlerName, string(result))
	}()

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	w.Write([]byte(fmt.Sprintf(`{"status":"processing","handler":"%s"}`, handlerName)))
}

func Run(cfg Config) error {
	if err := InitSecrets(); err != nil {
		return fmt.Errorf("failed to initialize secrets: %w", err)
	}
	server := NewServer(cfg)
	addr := fmt.Sprintf(":%d", cfg.Port)
	log.Printf("SONG server starting on %s", addr)
	return http.ListenAndServe(addr, server)
}

type StaticServer struct {
	root        http.Dir
	cacheMaxAge time.Duration
	indexFiles  []string
}

type StaticConfig struct {
	RootDir     string
	CacheMaxAge time.Duration
	IndexFiles  []string
}

func DefaultStaticConfig() StaticConfig {
	return StaticConfig{RootDir: "./public", CacheMaxAge: 24 * time.Hour, IndexFiles: []string{"index.html", "index.htm"}}
}

func NewStaticServer(cfg StaticConfig) *StaticServer {
	if _, err := os.Stat(cfg.RootDir); os.IsNotExist(err) {
		os.MkdirAll(cfg.RootDir, 0755)
	}
	return &StaticServer{
		root: http.Dir(cfg.RootDir), cacheMaxAge: cfg.CacheMaxAge, indexFiles: cfg.IndexFiles,
	}
}

func (ss *StaticServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", fmt.Sprintf("public, max-age=%d", int(ss.cacheMaxAge.Seconds())))
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("X-Frame-Options", "DENY")
	http.FileServer(ss.root).ServeHTTP(w, r)
}

func (ss *StaticServer) FileExists(path string) bool {
	fullPath := filepath.Join(string(ss.root), path)
	info, err := os.Stat(fullPath)
	if err != nil {
		return false
	}
	return !info.IsDir()
}

func detectContentType(path string) string {
	ext := strings.ToLower(filepath.Ext(path))
	switch ext {
	case ".html", ".htm":
		return "text/html; charset=utf-8"
	case ".css":
		return "text/css; charset=utf-8"
	case ".js":
		return "application/javascript; charset=utf-8"
	case ".json":
		return "application/json"
	case ".png":
		return "image/png"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".gif":
		return "image/gif"
	case ".svg":
		return "image/svg+xml"
	case ".ico":
		return "image/x-icon"
	case ".pdf":
		return "application/pdf"
	case ".xml":
		return "application/xml"
	case ".txt":
		return "text/plain; charset=utf-8"
	case ".woff":
		return "font/woff"
	case ".woff2":
		return "font/woff2"
	default:
		return "application/octet-stream"
	}
}
