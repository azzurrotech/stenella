package main

import (
	"encoding/json"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"azzurrotech/atp"
)

// ---------------------------------------------------------------------------
// STX08 — the atp console
//
// The console is the "easy-to-use interface" for atp itself: one document, and
// one endpoint per panel, that reports every surface of the server as it is
// actually running. The test drives the console over HTTP, the way the front
// end does.
// ---------------------------------------------------------------------------

// consolePanels is every panel name the console must answer, with the alias(es)
// the front end is allowed to use for it.
var consolePanels = []string{
	"platform", "subprojects", "apps", "functions", "secrets", "head",
	"headers", "song", "pod", "shepherd", "firewall", "static", "counts",
}

func TestConsoleReportsEveryATPSurface(t *testing.T) {
	srv, app := newTestPlatform(t)

	code, body := call(t, srv, http.MethodGet, "/stenella/api/console", "")
	if code != http.StatusOK {
		t.Fatalf("console: %d %s", code, body)
	}
	doc := mustJSON(t, body)

	// every documented panel is present in the one document
	for _, key := range consolePanels {
		if _, ok := doc[key]; !ok {
			t.Errorf("the console document has no %q panel", key)
		}
	}
	if t.Failed() {
		t.Fatalf("console document keys: %v", sortedKeys(doc))
	}

	// --- platform ---------------------------------------------------------
	platform := doc["platform"].(map[string]any)
	if platform["name"] != "atp" || platform["app"] != "stenella" {
		t.Errorf("platform = %v", platform)
	}
	if platform["atpVersion"] != atp.Version {
		t.Errorf("platform atpVersion = %v, want %v", platform["atpVersion"], atp.Version)
	}
	if platform["addr"] != srv.Addr() || platform["root"] != srv.Root() {
		t.Errorf("platform does not describe the running server: %v", platform)
	}
	if platform["encrypted"] != srv.Encrypted() {
		t.Errorf("platform encrypted = %v, want %v", platform["encrypted"], srv.Encrypted())
	}

	// --- sub-projects: atp and the three it embeds ------------------------
	subs := doc["subprojects"].([]any)
	if len(subs) != 4 {
		t.Fatalf("subprojects = %d, want atp+3", len(subs))
	}
	wantModules := []string{"azzurrotech/atp", "azzurrotech/pod", "azzurrotech/song", "azzurrotech/shepherd"}
	for i, want := range wantModules {
		sp := subs[i].(map[string]any)
		if sp["module"] != want {
			t.Errorf("subprojects[%d] = %v, want %s", i, sp["module"], want)
		}
		if mounts, _ := sp["mounts"].([]any); len(mounts) == 0 {
			t.Errorf("sub-project %s reports no mounts", want)
		}
		if sp["purpose"] == "" || sp["purpose"] == nil {
			t.Errorf("sub-project %s has no stated purpose", want)
		}
	}

	// --- apps -------------------------------------------------------------
	apps := doc["apps"].([]any)
	if len(apps) == 0 {
		t.Error("the console reports no hosted applications")
	}
	found := false
	for _, a := range apps {
		if a.(map[string]any)["name"] == "stenella" {
			found = true
		}
	}
	if !found {
		t.Errorf("the stenella application is not listed: %v", apps)
	}

	// --- functions: the registry -----------------------------------------
	functions := doc["functions"].([]any)
	if len(functions) < 40 {
		t.Errorf("only %d functions registered", len(functions))
	}
	haveRoute := func(method, pattern string) bool {
		for _, f := range functions {
			m := f.(map[string]any)
			if m["method"] == method && m["pattern"] == pattern {
				return true
			}
		}
		return false
	}
	for _, want := range [][2]string{
		{"GET", "/stenella/api/items"},
		{"GET", "/stenella/feeds/{name}"},
		{"GET", "/stenella/api/console"},
	} {
		if !haveRoute(want[0], want[1]) {
			t.Errorf("%s %s is missing from the registry listing", want[0], want[1])
		}
	}

	// --- head -------------------------------------------------------------
	head := doc["head"].(map[string]any)
	if head["title"] != "stenella test" {
		t.Errorf("head title = %v", head["title"])
	}
	if _, ok := head["global"].([]any); !ok {
		t.Errorf("head global is not a list: %v", head["global"])
	}
	if _, ok := head["perApp"].(map[string]any); !ok {
		t.Errorf("head perApp is not an object: %v", head["perApp"])
	}

	// --- headers ----------------------------------------------------------
	if headers, ok := doc["headers"].([]any); !ok || len(headers) == 0 {
		t.Errorf("headers = %v, want the default rule set", doc["headers"])
	}

	// --- song -------------------------------------------------------------
	song := doc["song"].(map[string]any)
	if song["root"] != srv.Song().Store().Root() {
		t.Errorf("song root = %v", song["root"])
	}
	if comps, _ := song["components"].([]any); len(comps) == 0 {
		t.Errorf("song reports no components: %v", song)
	}

	// --- pod: the record store, its real tables --------------------------
	podPanel := doc["pod"].(map[string]any)
	tables := podPanel["tables"].([]any)
	names := map[string]bool{}
	for _, tb := range tables {
		// pod namespaces a table with its owner, e.g. "stenella/items"
		name := tb.(map[string]any)["name"].(string)
		names[name] = true
		names[strings.TrimPrefix(name, "stenella/")] = true
	}
	for _, want := range []string{"items", "sources", "rules", "insights", "notes", "entities"} {
		if !names[want] {
			t.Errorf("pod panel is missing the %q table (has %v)", want, sortedKeys2(names))
		}
	}

	// --- shepherd ---------------------------------------------------------
	sh := doc["shepherd"].(map[string]any)
	if sh["issuer"] != srv.Shepherd().Issuer() || sh["gateway"] != "/gw/" {
		t.Errorf("shepherd = %v", sh)
	}
	if caps, _ := sh["capabilities"].([]any); len(caps) < 5 {
		t.Errorf("shepherd lists %v capabilities, want the full set", sh["capabilities"])
	}

	// --- firewall ---------------------------------------------------------
	fw := doc["firewall"].(map[string]any)
	if _, ok := fw["defaultAllow"].(bool); !ok {
		t.Errorf("firewall defaultAllow = %v", fw["defaultAllow"])
	}
	if _, ok := fw["rules"].([]any); !ok {
		t.Errorf("firewall rules = %v", fw["rules"])
	}

	// --- static -----------------------------------------------------------
	if _, ok := doc["static"].([]any); !ok {
		t.Errorf("static is not a list: %v", doc["static"])
	}

	// --- counts: the store's own tally, not a re-count --------------------
	counts := doc["counts"].(map[string]any)
	wantCounts := app.store.Counts()
	if len(wantCounts) == 0 {
		t.Fatal("the store reports no counts")
	}
	for k, want := range wantCounts {
		if got, ok := counts[k].(float64); !ok || int(got) != want {
			t.Errorf("counts[%q] = %v, want %d", k, counts[k], want)
		}
	}
}

func TestConsoleSurfaceEndpointsMatchTheDocument(t *testing.T) {
	srv, _ := newTestPlatform(t)

	_, body := call(t, srv, http.MethodGet, "/stenella/api/console", "")
	doc := mustJSON(t, body)

	// A few panels are wrapped in a named object by their own endpoint; the
	// helper mirrors that so a panel and its endpoint can be compared directly.
	wrap := func(panel string, whole any) any {
		switch panel {
		case "functions", "static", "subprojects", "apps", "headers":
			return map[string]any{panel: whole}
		}
		return whole
	}

	// each panel is available on its own, and answers the same data
	for _, panel := range consolePanels {
		code, surfaceBody := call(t, srv, http.MethodGet, "/stenella/api/console/"+panel, "")
		if code != http.StatusOK {
			t.Errorf("GET /stenella/api/console/%s = %d %s", panel, code, surfaceBody)
			continue
		}
		var got any
		if err := json.Unmarshal([]byte(surfaceBody), &got); err != nil {
			t.Errorf("console/%s is not JSON: %v", panel, err)
			continue
		}
		if !sameJSON(got, wrap(panel, doc[panel])) {
			t.Errorf("console/%s does not match the %s panel of the whole document", panel, panel)
		}
	}

	// the aliases the front end may use
	for alias, panel := range map[string]string{
		"sub-projects": "subprojects",
		"registry":     "functions",
		"vault":        "secrets",
	} {
		code, body := call(t, srv, http.MethodGet, "/stenella/api/console/"+alias, "")
		if code != http.StatusOK {
			t.Errorf("alias %s = %d", alias, code)
			continue
		}
		if !sameJSON(mustJSON(t, body), wrap(panel, doc[panel])) {
			t.Errorf("alias %s does not answer the %s panel", alias, panel)
		}
	}

	// an unknown surface is a 404 with a readable message, not a panic or a
	// silent empty document
	code, body := call(t, srv, http.MethodGet, "/stenella/api/console/nonsense", "")
	if code != http.StatusNotFound {
		t.Errorf("/console/nonsense = %d, want 404 (%s)", code, body)
	}
	if !strings.Contains(body, "nonsense") {
		t.Errorf("the 404 does not name the unknown surface: %s", body)
	}
}

// The console is a management surface: it may name a secret, but it must never
// carry a secret value — not over HTTP, and not in the files it reads.
func TestConsoleNeverLeaksASecretValue(t *testing.T) {
	srv, _ := newTestPlatform(t)

	const (
		secretApp   = "stenella"
		secretName  = "upstream-api-key"
		secretValue = "SUPER-SECRET-VALUE-1234567890"
	)
	if err := srv.Secrets().Set(secretApp, secretName, secretValue, "used by a test"); err != nil {
		t.Fatalf("set secret: %v", err)
	}

	// the secret is named in the panel, with its note, and no value
	code, body := call(t, srv, http.MethodGet, "/stenella/api/console/secrets", "")
	if code != http.StatusOK {
		t.Fatalf("secrets panel: %d %s", code, body)
	}
	if !strings.Contains(body, secretName) || !strings.Contains(body, "used by a test") {
		t.Errorf("the secrets panel does not name the stored secret: %s", body)
	}

	// sweep the whole document and every surface
	var bodies []string
	_, whole := call(t, srv, http.MethodGet, "/stenella/api/console", "")
	bodies = append(bodies, whole)
	for _, panel := range append(append([]string{}, consolePanels...), "vault", "registry", "sub-projects") {
		_, b := call(t, srv, http.MethodGet, "/stenella/api/console/"+panel, "")
		bodies = append(bodies, b)
	}
	for i, b := range bodies {
		if strings.Contains(b, secretValue) {
			t.Fatalf("console response %d leaks the secret value:\n%s", i, b)
		}
	}

	// and the value is not sitting in plaintext on disk either
	vaultDir := filepath.Join(srv.Root(), "secrets")
	err := filepath.WalkDir(vaultDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if strings.Contains(string(raw), secretValue) {
			t.Errorf("the vault file %s holds the secret in plaintext", path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk vault: %v", err)
	}

	// the master secret itself must not appear anywhere in the console either
	for i, b := range bodies {
		if strings.Contains(b, "test-master-secret-at-least-32-bytes!!") {
			t.Fatalf("console response %d exposes the master secret", i)
		}
	}
}

// ---------------------------------------------------------------------------
// small helpers
// ---------------------------------------------------------------------------

func sortedKeys(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func sortedKeys2(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// sameJSON compares two decoded JSON values structurally.
func sameJSON(a, b any) bool {
	ab, err1 := json.Marshal(a)
	bb, err2 := json.Marshal(b)
	if err1 != nil || err2 != nil {
		return false
	}
	return string(ab) == string(bb)
}
