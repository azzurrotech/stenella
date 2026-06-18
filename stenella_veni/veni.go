package stenella_veni

import (
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

type middleware func(http.Handler) http.Handler

func UseMiddleware(final http.Handler, mws ...middleware) http.Handler {
	h := final
	for i := len(mws) - 1; i >= 0; i-- {
		h = mws[i](h)
	}
	return h
}

func LoggingMw(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		log.Printf("%s %s %s", r.RemoteAddr, r.Method, r.URL.Path)
		next.ServeHTTP(w, r)
	})
}

func MainHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		w.Header().Set("Allow", "GET, POST")
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}

	if r.Method == http.MethodGet {
		dir := os.Getenv("VENI")
		if dir == "" {
			http.Error(w, "Server mis‑configuration: VENI env var not set", http.StatusInternalServerError)
			return
		}
		fs := http.FileServer(http.Dir(dir))
		http.StripPrefix("/", fs).ServeHTTP(w, r)
		return
	}

	if err := r.ParseMultipartForm(10 << 20); err != nil {
		http.Error(w, "Failed to parse form data: "+err.Error(), http.StatusBadRequest)
		return
	}

	venMethod := r.FormValue("VENI_METHOD")
	if venMethod == "" {
		http.Error(w, "Missing required field VENI_METHOD", http.StatusBadRequest)
		return
	}
	venURI := r.FormValue("VENI_URI")
	if venURI == "" {
		http.Error(w, "Missing required field VENI_URI", http.StatusBadRequest)
		return
	}

	allowedMethods := map[string]bool{
		http.MethodGet: true, http.MethodPost: true, http.MethodPut: true,
		http.MethodPatch: true, http.MethodDelete: true, http.MethodHead: true,
		http.MethodOptions: true,
	}
	if !allowedMethods[strings.ToUpper(venMethod)] {
		http.Error(w, "Unsupported VENI_METHOD: "+venMethod, http.StatusBadRequest)
		return
	}

	venBody := r.FormValue("VENI_BODY")
	var bodyReader io.Reader
	if venBody != "" {
		bodyReader = strings.NewReader(venBody)
	}

	venHeaders := make(map[string]string)
	for key, values := range r.PostForm {
		if strings.HasPrefix(key, "VENI_HEADER__") {
			headerName := strings.TrimPrefix(key, "VENI_HEADER__")
			venHeaders[headerName] = strings.Join(values, ",")
		}
	}

	outReq, err := http.NewRequest(strings.ToUpper(venMethod), venURI, bodyReader)
	if err != nil {
		http.Error(w, "Failed to create outbound request: "+err.Error(), http.StatusBadRequest)
		return
	}
	for k, v := range venHeaders {
		outReq.Header.Set(k, v)
	}
	if venBody != "" && outReq.Header.Get("Content-Type") == "" {
		outReq.Header.Set("Content-Type", "text/plain")
	}

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(outReq)
	if err != nil {
		http.Error(w, "Outbound request failed: "+err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	for k, vv := range resp.Header {
		for _, v := range vv {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	if _, err := io.Copy(w, resp.Body); err != nil {
		log.Printf("error streaming response body: %v", err)
	}
}

func ProxyRequest(method, targetURL string, headers map[string]string, body io.Reader) (*http.Response, error) {
	u, err := url.Parse(targetURL)
	if err != nil {
		return nil, err
	}
	if !u.IsAbs() {
		return nil, fmt.Errorf("absolute URL required: %s", targetURL)
	}

	outReq, err := http.NewRequest(method, targetURL, body)
	if err != nil {
		return nil, err
	}
	for k, v := range headers {
		outReq.Header.Set(k, v)
	}

	return http.DefaultClient.Do(outReq)
}
