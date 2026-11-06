// Command stenella runs the stenella OSINT platform as a standalone service.
//
// Stenella owns its HTTP surface. The four platform services it uses — atp,
// pod, song and shepherd — are reached over HTTP at the URLs below, so the
// binary can be deployed beside them rather than linked into them.
package main

import (
	"embed"
	"flag"
	"log"
	"net/http"
	"os"
	"path/filepath"
)

// The assets that ship. The list is explicit rather than "web" so the browser
// tests under web/tests/ are never served or precached by the service worker.
//
//go:embed web/index.html web/app.css web/app.js web/lib.js web/world.js web/icon.svg
var webFS embed.FS

func main() {
	var (
		addr        = flag.String("addr", env("STENELLA_ADDR", ":8085"), "listen address (host:port)")
		root        = flag.String("root", env("STENELLA_ROOT", "./data"), "root for the hosted app and local state")
		podURL      = flag.String("pod", env("POD_URL", "http://127.0.0.1:8081"), "pod service URL")
		songURL     = flag.String("song", env("SONG_URL", "http://127.0.0.1:8082"), "song service URL")
		shepherdURL = flag.String("shepherd", env("SHEPHERD_URL", "http://127.0.0.1:8083"), "shepherd service URL")
		shepherdAdm = flag.String("shepherd-admin", env("SHEPHERD_ADMIN", ""), "shepherd admin token")
		atpURL      = flag.String("atp", env("ATP_URL", "http://127.0.0.1:8080"), "atp service URL")
	)
	flag.Parse()

	srv, err := New(Options{
		Addr:          *addr,
		Root:          *root,
		Title:         "stenella — OSINT platform",
		PodURL:        *podURL,
		SongURL:       *songURL,
		ShepherdURL:   *shepherdURL,
		ShepherdAdmin: *shepherdAdm,
		ATPURL:        *atpURL,
	})
	if err != nil {
		log.Fatalf("stenella: %v", err)
	}
	defer srv.Close()

	// --- the hosted application -------------------------------------------
	if err := srv.Apps().Register(AppMeta{
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

	// --- the platform: stenella's own routes ------------------------------
	app, err := NewApp(srv)
	if err != nil {
		log.Fatalf("stenella: %v", err)
	}
	if err := app.Register(); err != nil {
		log.Fatalf("stenella: %v", err)
	}

	log.Printf("stenella %s listening on %s (root %s)", Version, srv.Addr(), srv.Root())
	log.Printf("  services: atp %s pod %s song %s shepherd %s", srv.ATP().Base(), srv.Pod().Base(), srv.Song().Base(), srv.Shepherd().Base())
	log.Printf("  app: http://%s/stenella/", hostPort(srv.Addr()))
	if err := http.ListenAndServe(srv.Addr(), srv); err != nil {
		log.Fatal(err)
	}
}
