// testenv_test.go — the in-process service stack the tests run against.
//
// Stenella is a service now: it owns its HTTP surface and reaches pod, song,
// shepherd and atp over the network. The tests therefore run the real services
// on loopback ports rather than linking them into the test binary. A handler
// that works here works when the five services are deployed side by side.
//
// pod, song and atp are importable libraries, so they run in-process. shepherd
// ships as a command (its server lives in package main, by design), so the test
// builds that command once and runs one instance per stack.
package main

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"azzurrotech/atp"
	"azzurrotech/pod"
	"azzurrotech/song/pkg/song"
)

// testStack holds a running five-service deployment for one test.
type testStack struct {
	srv *Server
	app *App

	podURL      string
	songURL     string
	shepherdURL string
	atpURL      string

	// Facts the console must report, captured from the services themselves.
	songRoot   string
	atpRoot    string
	atpSecret  string
	adminToken string
}

// newTestStack starts pod, song, shepherd and atp on ephemeral loopback ports
// and builds stenella in front of them, exactly as main does.
func newTestStack(t *testing.T) *testStack {
	t.Helper()
	root := t.TempDir()
	st := &testStack{atpSecret: "test-master-secret-at-least-32-bytes!!"}

	// --- pod: the record store -------------------------------------------
	podRoot := filepath.Join(root, "pod")
	store, err := pod.Open(podRoot)
	if err != nil {
		t.Fatalf("pod.Open: %v", err)
	}
	noUI := false
	podHandler := pod.NewHandler(pod.HandlerOptions{Store: store, Mount: "/", UI: &noUI})
	if err := podHandler.Indexer().Rebuild(); err != nil {
		t.Fatalf("pod index rebuild: %v", err)
	}
	podHTTP := httptest.NewServer(podHandler)
	t.Cleanup(podHTTP.Close)
	st.podURL = podHTTP.URL

	// --- song: the static silo host --------------------------------------
	songRoot := filepath.Join(root, "song")
	songSrv, err := song.New(song.Config{Root: songRoot})
	if err != nil {
		t.Fatalf("song.New: %v", err)
	}
	songHTTP := httptest.NewServer(songSrv.Handler())
	t.Cleanup(songHTTP.Close)
	st.songURL = songHTTP.URL
	st.songRoot = songSrv.Store().Root()

	// --- shepherd: capabilities, firewall, gateway -----------------------
	st.shepherdURL, st.adminToken = startShepherd(t)

	// --- atp: functions, vault, head/header/static ------------------------
	atpRoot := filepath.Join(root, "atp")
	atpSrv, err := atp.New(atp.Options{
		Root:          atpRoot,
		Addr:          ":0",
		Secret:        st.atpSecret,
		Title:         "stenella test",
		PodURL:        st.podURL,
		SongURL:       st.songURL,
		ShepherdURL:   st.shepherdURL,
		ShepherdAdmin: st.adminToken,
	})
	if err != nil {
		t.Fatalf("atp.New: %v", err)
	}
	atpHTTP := httptest.NewServer(atpSrv)
	t.Cleanup(atpHTTP.Close)
	st.atpURL = atpHTTP.URL
	st.atpRoot = atpRoot

	// --- stenella: this package, in front of all four --------------------
	srv, err := New(Options{
		Addr:          ":0",
		Root:          filepath.Join(root, "stenella"),
		Title:         "stenella test",
		PodURL:        st.podURL,
		SongURL:       st.songURL,
		ShepherdURL:   st.shepherdURL,
		ShepherdAdmin: st.adminToken,
		ATPURL:        st.atpURL,
	})
	if err != nil {
		t.Fatalf("stenella.New: %v", err)
	}
	t.Cleanup(func() { _ = srv.Close() })

	// register the hosted application exactly as main does, so the console's
	// app/head panels report the same thing in a test as in production
	if err := srv.Apps().Register(AppMeta{
		Name:        "stenella",
		Title:       "stenella",
		ShortName:   "stenella",
		Description: "Open-source intelligence platform",
		ThemeColor:  "#0a84ff",
		Search:      "/stenella/?q={searchTerms}",
	}); err != nil {
		t.Fatalf("app register: %v", err)
	}
	srv.Head().Title("stenella test")
	srv.Head().Add(`<meta name="description" content="stenella: aggregated open-source intelligence">`)
	srv.Headers().
		Request("X-Stenella-*").Strip().
		Response("/stenella/*.css").Set("Cache-Control", "public, max-age=3600").
		Response("/stenella/*.js").Set("Cache-Control", "public, max-age=3600").
		Internal("X-Stenella-View").FromPath(1)

	app, err := NewApp(srv)
	if err != nil {
		t.Fatalf("NewApp: %v", err)
	}
	if err := app.Register(); err != nil {
		t.Fatalf("Register: %v", err)
	}
	st.srv = srv
	st.app = app
	return st
}

// newTestPlatform is the common shorthand: the stenella server and platform.
func newTestPlatform(t *testing.T) (*Server, *App) {
	t.Helper()
	st := newTestStack(t)
	return st.srv, st.app
}

// newTestStore returns a store over a fresh, running pod server.
func newTestStore(t *testing.T) *Store {
	t.Helper()
	store, err := pod.Open(filepath.Join(t.TempDir(), "pod"))
	if err != nil {
		t.Fatalf("pod.Open: %v", err)
	}
	noUI := false
	h := pod.NewHandler(pod.HandlerOptions{Store: store, Mount: "/", UI: &noUI})
	httpSrv := httptest.NewServer(h)
	t.Cleanup(httpSrv.Close)

	s, err := NewStore(NewPodClient(httpSrv.URL))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	return s
}

// ---------------------------------------------------------------------------
// the shepherd binary
// ---------------------------------------------------------------------------

var (
	shepherdBuildOnce sync.Once
	shepherdBin       string
	shepherdBuildErr  error
)

// shepherdBinary builds azzurrotech/shepherd once per test run.
func shepherdBinary(t *testing.T) string {
	t.Helper()
	shepherdBuildOnce.Do(func() {
		dir, err := os.MkdirTemp("", "stenella-shepherd-")
		if err != nil {
			shepherdBuildErr = err
			return
		}
		out := filepath.Join(dir, "shepherd")
		cmd := exec.Command("go", "build", "-o", out, "azzurrotech/shepherd")
		combined, err := cmd.CombinedOutput()
		if err != nil {
			shepherdBuildErr = fmt.Errorf("go build azzurrotech/shepherd: %v\n%s", err, combined)
			return
		}
		shepherdBin = out
	})
	if shepherdBuildErr != nil {
		t.Fatalf("shepherd binary: %v", shepherdBuildErr)
	}
	return shepherdBin
}

// startShepherd runs the real shepherd server on a loopback port and returns
// its base URL and admin token (printed by its startup banner).
func startShepherd(t *testing.T) (url, admin string) {
	t.Helper()
	bin := shepherdBinary(t)
	port := freePort(t)
	base := "http://127.0.0.1:" + strconv.Itoa(port)

	cmd := exec.Command(bin,
		"-port", strconv.Itoa(port),
		"-issuer", "azzurrotech/shepherd",
		"-secret", "test-shepherd-secret-at-least-16-bytes",
		"-rate", "0",
		"-debounce", "0",
	)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("shepherd stdout: %v", err)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("start shepherd: %v", err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	})

	tokenCh := make(chan string, 1)
	go func() {
		sc := bufio.NewScanner(stdout)
		sent := false
		for sc.Scan() {
			line := sc.Text()
			if !sent && strings.Contains(line, "admin token") {
				if i := strings.LastIndex(line, ":"); i >= 0 {
					tokenCh <- strings.TrimSpace(line[i+1:])
					sent = true
				}
			}
		}
		if !sent {
			tokenCh <- ""
		}
		_, _ = io.Copy(io.Discard, stdout)
	}()

	select {
	case admin = <-tokenCh:
	case <-time.After(10 * time.Second):
		t.Fatalf("shepherd did not print its admin token; stderr: %s", stderr.String())
	}
	if admin == "" {
		t.Fatalf("shepherd printed no admin token; stderr: %s", stderr.String())
	}
	waitForHealth(t, base+"/health")
	return base, admin
}

// freePort reserves an ephemeral port and releases it for the child process.
func freePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("free port: %v", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()
	return port
}

// waitForHealth polls url until it answers 2xx or the deadline passes.
func waitForHealth(t *testing.T, url string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	client := &http.Client{Timeout: 500 * time.Millisecond}
	for time.Now().Before(deadline) {
		resp, err := client.Get(url)
		if err == nil {
			_, _ = io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
			if resp.StatusCode/100 == 2 {
				return
			}
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("%s did not become healthy", url)
}

// callRemote performs one request against a service URL and returns the status
// and body. It is how a test reaches a service stenella talks to but does not
// mount (shepherd's verify/revoke endpoints, for example).
func callRemote(t *testing.T, url, method, body string, hdr map[string]string) (int, string) {
	t.Helper()
	var rdr io.Reader
	if body != "" {
		rdr = strings.NewReader(body)
	}
	req, err := http.NewRequest(method, url, rdr)
	if err != nil {
		t.Fatalf("request %s %s: %v", method, url, err)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, url, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read %s %s: %v", method, url, err)
	}
	return resp.StatusCode, string(raw)
}
