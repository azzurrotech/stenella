package web

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// The platform's dependency rule is that pod, shepherd and song run fully
// standalone on the standard library, and that only ATP composes them. stenella
// vendors those three trees, so the rule is stenella's to keep honest: a change
// that made pod import song would still compile inside this repository, and
// would break every standalone deployment of pod.
//
// scripts/standalone.sh checks the same properties and can report per-module
// detail. This test exists so `go test ./...` alone is enough to catch a
// regression — a gate nobody runs is not a gate.

func vendoredCore(t *testing.T, name string) string {
	t.Helper()
	dir := filepath.Join("..", "atp", name)
	if _, err := os.Stat(filepath.Join(dir, "go.mod")); err != nil {
		t.Fatalf("vendored %s has no go.mod (%v); the standalone gate cannot be checked", name, err)
	}
	return dir
}

var directiveLine = regexp.MustCompile(`(?m)^\s*(require|replace|exclude)\b.*$`)

// A core module must require nothing. This asks the toolchain rather than
// pattern-matching go.mod: our module paths ("azzurrotech/pod") contain no dot
// anywhere, so a "looks like a domain" heuristic would read a sibling import as
// a local package and pass. go list is the only thing here that knows the
// difference.
func TestCoreModulesRequireNothing(t *testing.T) {
	for _, name := range []string{"pod", "shepherd", "song"} {
		dir := vendoredCore(t, name)
		out, err := run(t, dir, "go", "list", "-m", "-f", "{{if not .Main}}{{.Path}}{{end}}", "all")
		if err != nil {
			t.Errorf("atp/%s: go list -m all: %v\n%s", name, err, out)
			continue
		}
		var deps []string
		for _, line := range strings.Split(out, "\n") {
			if line = strings.TrimSpace(line); line != "" {
				deps = append(deps, line)
			}
		}
		if len(deps) > 0 {
			t.Errorf("atp/%s is in a build list with %s; the core modules must stay dependency-free",
				name, strings.Join(deps, ", "))
		}

		// And go.mod itself must carry no dependency directive. A replace with no
		// matching require is inert, but it is a declared intention to share, and
		// it is what someone would fill in next.
		raw, err := os.ReadFile(filepath.Join(dir, "go.mod"))
		if err != nil {
			t.Fatalf("read %s/go.mod: %v", name, err)
		}
		if m := directiveLine.FindAllString(string(raw), -1); len(m) > 0 {
			t.Errorf("atp/%s/go.mod has a dependency directive: %s",
				name, strings.Join(m, " | "))
		}
	}
}

// And the module path must be its own name, not a package inside a parent —
// which is what would make `go build` inside it resolve against the wrong
// module graph.
func TestCoreModulesAreSeparateModules(t *testing.T) {
	for name, want := range map[string]string{
		"pod":      "azzurrotech/pod",
		"shepherd": "azzurrotech/shepherd",
		"song":     "azzurrotech/song",
	} {
		dir := vendoredCore(t, name)
		out, err := run(t, dir, "go", "list", "-m")
		if err != nil {
			t.Errorf("atp/%s: go list -m: %v", name, err)
			continue
		}
		if got := strings.TrimSpace(out); got != want {
			t.Errorf("atp/%s module path = %q, want %q", name, got, want)
		}
	}
}

// The import graph must be standard library only. This is the check that
// actually catches an import: go.mod can look clean while a source file imports
// something, and .Standard is the toolchain's own answer rather than a heuristic
// over path shapes.
func TestCoreModulesImportOnlyStdlib(t *testing.T) {
	for _, name := range []string{"pod", "shepherd", "song"} {
		dir := vendoredCore(t, name)
		mod, err := run(t, dir, "go", "list", "-m")
		if err != nil {
			t.Errorf("atp/%s: go list -m: %v", name, err)
			continue
		}
		mod = strings.TrimSpace(mod)
		out, err := run(t, dir, "go", "list", "-deps", "-f", "{{if not .Standard}}{{.ImportPath}}{{end}}", "./...")
		if err != nil {
			t.Errorf("atp/%s: go list -deps: %v", name, err)
			continue
		}
		var foreign []string
		for _, line := range strings.Split(out, "\n") {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			if line == mod || strings.HasPrefix(line, mod+"/") {
				continue
			}
			foreign = append(foreign, line)
		}
		if len(foreign) > 0 {
			t.Errorf("atp/%s imports non-stdlib packages: %s", name, strings.Join(foreign, ", "))
		}
	}
}

// Each core builds on its own. This is the literal standalone claim, and it is
// the one that would break first if two of them started sharing code.
func TestCoreModulesBuildIndependently(t *testing.T) {
	if testing.Short() {
		t.Skip("builds three modules; skipped under -short")
	}
	for _, name := range []string{"pod", "shepherd", "song"} {
		dir := vendoredCore(t, name)
		if out, err := run(t, dir, "go", "build", "./..."); err != nil {
			t.Errorf("atp/%s does not build standalone: %v\n%s", name, err, out)
		}
	}
}

// run executes a command in dir and returns its combined output, so a failure
// carries the toolchain's own message rather than just an exit code.
func run(t *testing.T, dir string, name string, args ...string) (string, error) {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	return string(out), err
}
