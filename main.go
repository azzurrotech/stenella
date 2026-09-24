// Package main runs stenella, the AzzurroTech data platform.
//
// stenella embeds atp (which itself embeds song, pod and shepherd) and uses atp
// as middleware: atp owns /api, /clients, /c, /gw, /login and /health, while
// stenella owns "/" (the search/link homepage) and the /s/* workspace — the
// client portal, the super-admin console, public combined feeds and share URLs.
//
// Every song/pod/shepherd feature exposed by the UI is reached through atp's own
// HTTP handler (see the atpclient package) — never called directly.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"

	"azzurrotech/stenella/web"
)

func main() {
	port := flag.String("port", "8084", "listen port")
	root := flag.String("root", "./data", "shared data root (clients, secrets, song+pod stores, feeds, links)")
	secret := flag.String("secret", "", "master secret, at least 32 bytes (or $STENELLA_SECRET)")
	adminUser := flag.String("admin-user", "admin", "atp administrator username")
	adminPass := flag.String("admin-pass", "", "atp administrator password (or $STENELLA_ADMIN_PASSWORD)")
	base := flag.String("public-base", "", "absolute base URL used in share links, e.g. https://azzurro.tech")
	libsDir := flag.String("libs-dir", "static", "directory holding the veni/vidi/vici/vini submodule worktrees")
	help := flag.Bool("help", false, "show usage")
	flag.Parse()

	if *help {
		flag.Usage()
		fmt.Print(`
stenella — data platform on top of atp.

  --port        listen port            (default 8084)
  --root        shared data root       (default ./data)
  --secret      master secret ≥32B     (required; or $STENELLA_SECRET)
  --admin-user  atp admin user         (default admin)
  --admin-pass  atp admin password     (default admin; or $STENELLA_ADMIN_PASSWORD)
  --public-base absolute base for share links, e.g. https://azzurro.tech
  --libs-dir    dir with the veni/vidi/vici/vini libraries (default static)

Routes atp owns: /api, /clients, /c, /gw, /login, /logout, /health.
Routes stenella owns: / (homepage), /s/portal, /s/admin, /s/feed/**,
/s/x/**, /s/api/**.
`)
		return
	}
	if *secret == "" {
		*secret = os.Getenv("STENELLA_SECRET")
	}
	if *adminPass == "" {
		*adminPass = os.Getenv("STENELLA_ADMIN_PASSWORD")
	}
	if *secret == "" {
		log.Fatal("--secret (or $STENELLA_SECRET) is required: a random string of at least 32 bytes")
	}

	libs, err := loadLibs(*libsDir)
	if err != nil {
		log.Fatalf("load JS libraries: %v", err)
	}

	svc, err := web.New(web.Config{
		Root:          *root,
		AtpSecret:     *secret,
		AdminUser:     *adminUser,
		AdminPassword: *adminPass,
		PublicBase:    *base,
		Libs:          libs,
	})
	if err != nil {
		log.Fatalf("stenella: %v", err)
	}

	go svc.Background(context.Background())

	addr := ":" + *port
	log.Printf("stenella listening on %s (data root %q)", addr, *root)
	if err := http.ListenAndServe(addr, svc.Handler()); err != nil {
		log.Fatal(err)
	}
}

// loadLibs reads the four Emperor42 libraries from their submodule worktrees
// (they are separate modules, so Go's //go:embed cannot reach them; the
// Dockerfile copies them into the image instead) so the web layer can serve
// them at /s/static/lib/*.
func loadLibs(dir string) (map[string][]byte, error) {
	names := []string{"veni", "vidi", "vici", "vini"}
	out := make(map[string][]byte, len(names))
	for _, n := range names {
		b, err := os.ReadFile(filepath.Join(dir, n, n+".js"))
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", n, err)
		}
		out[n+".js"] = b
	}
	return out, nil
}
