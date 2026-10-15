// Command stenella runs the stenella OSINT platform **on top of atp**: atp is
// the server, stenella contributes the hosted dashboard, its Go functions and
// its secrets. This is the integration the atp README describes in
// "Using atp from another Go project".
package main

import (
	"embed"
	"flag"
	"log"
	"net/http"
	"os"
	"path/filepath"

	"azzurrotech/atp"
)

// The assets that ship. The list is explicit rather than "web" so the browser
// tests under web/tests/ are never served or precached by the service worker.
//
//go:embed web/index.html web/app.css web/app.js web/lib.js web/world.js web/icon.svg
var webFS embed.FS

func main() {
	var (
		addr   = flag.String("addr", env("ATP_ADDR", ":8080"), "listen address (host:port)")
		root   = flag.String("root", env("ATP_ROOT", "./data"), "root for the hosted app, the pod store and the vault")
		secret = flag.String("secret", env("ATP_SECRET", ""), "master secret, at least 32 bytes")
	)
	flag.Parse()

	// --- the platform is atp ----------------------------------------------
	srv, err := atp.New(atp.Options{
		Root:   *root,
		Addr:   *addr,
		Secret: *secret,
		Title:  "stenella — OSINT platform",
	})
	if err != nil {
		log.Fatalf("stenella: %v", err)
	}
	defer srv.Close()

	// --- the hosted application -------------------------------------------
	if err := srv.Apps().Register(atp.App{
		Name:        "stenella",
		Title:       "stenella",
		ShortName:   "stenella",
		Description: "Open-source intelligence platform",
		ThemeColor:  "#0a84ff",
		Search:      "/stenella/?q={searchTerms}",
	}); err != nil {
		log.Fatalf("stenella: app: %v", err)
	}

	appDir := filepath.Join(srv.Root(), "apps", "stenella")
	if err := materialiseWeb(webFS, appDir); err != nil {
		log.Fatalf("stenella: web assets: %v", err)
	}
	indexPath := filepath.Join(appDir, "index.html")
	if _, err := os.Stat(indexPath); os.IsNotExist(err) {
		// materialiseWeb should have created it if embedded
		log.Fatalf("stenella: index.html missing from embedded web assets")
	}

	// --- central head management ------------------------------------------
	srv.Head().Title("stenella — OSINT platform")
	srv.Head().Add(`<meta name="description" content="stenella: aggregated open-source intelligence">`)
	srv.Head().AddFor("stenella", `<meta name="color-scheme" content="light dark">`)

	// --- header policy -----------------------------------------------------
	srv.Headers().
		Request("X-Stenella-*").Strip().
		Response("/stenella/*.css").Set("Cache-Control", "public, max-age=3600").
		Response("/stenella/*.js").Set("Cache-Control", "public, max-age=3600").
		Internal("X-Stenella-View").FromPath(1)

	// --- secrets (API keys for external sources) ---------------------------
	// keep minimal

	// --- the platform: stenella on atp ------------------------------------
	app, err := NewApp(srv)
	if err != nil {
		log.Fatalf("stenella: %v", err)
	}
	if err := app.Register(); err != nil {
		log.Fatalf("stenella: %v", err)
	}

	log.Printf("stenella on atp %s listening on %s (root %s)", atp.Version, srv.Addr(), srv.Root())
	log.Printf("  platform: http://%s/     app: http://%s/stenella/", hostPort(srv.Addr()), hostPort(srv.Addr()))
	if err := http.ListenAndServe(srv.Addr(), srv); err != nil {
		log.Fatal(err)
	}
}
