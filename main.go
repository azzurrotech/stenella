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
	"strings"
	"time"

	"azzurrotech/stenella/netguard"
	"azzurrotech/stenella/web"
)

// envBool reads a boolean flag fallback from the environment. Only the literal
// value "1" enables it, so a stray value such as "false" or "almost" can never
// silently switch a security control on.
func envBool(name string) bool {
	return os.Getenv(name) == "1"
}

func main() {
	port := flag.String("port", "8084", "listen port")
	root := flag.String("root", "./data", "shared data root (clients, secrets, song+pod stores, feeds, links)")
	secret := flag.String("secret", "", "master secret, at least 32 bytes (or $STENELLA_SECRET)")
	adminUser := flag.String("admin-user", "admin", "atp administrator username")
	adminPass := flag.String("admin-pass", "", "atp administrator password (or $STENELLA_ADMIN_PASSWORD)")
	base := flag.String("public-base", "", "absolute base URL used in share links, e.g. https://azzurro.tech")
	libsDir := flag.String("libs-dir", "static", "directory holding the veni/vidi/vici/vini submodule worktrees")
	platformPath := flag.String("platform-path", "/platform", "path on mapped hosts that redirects to the stenella platform")
	hostSites := map[string]string{}
	flag.Func("host-site", "Serve a client's hosted site at / on a public host (host=client; repeatable).",
		func(v string) error {
			host, client, ok := strings.Cut(v, "=")
			if !ok || strings.TrimSpace(host) == "" || strings.TrimSpace(client) == "" {
				return fmt.Errorf("--host-site must look like azzurro.tech=azzurrotech, got %q", v)
			}
			hostSites[strings.ToLower(strings.TrimSpace(host))] = strings.TrimSpace(client)
			return nil
		})
	// Both switches are OFF by default: each one relaxes a security control
	// (proxy-header trust, SSRF guard), so they must only ever be enabled
	// deliberately by the operator.
	trustProxy := flag.Bool("trust-proxy", envBool("STENELLA_TRUST_PROXY"),
		"trust X-Forwarded-For for rate-limit keys — only behind a proxy you control (or $STENELLA_TRUST_PROXY=1)")
	allowPrivateFetch := flag.Bool("allow-private-fetch", envBool("STENELLA_ALLOW_PRIVATE_FETCH"),
		"let server-side feed/OPML fetches reach loopback and private addresses — dev/test only (or $STENELLA_ALLOW_PRIVATE_FETCH=1)")
	help := flag.Bool("help", false, "show usage")
	flag.Parse()

	if len(hostSites) == 0 {
		// Default: the azzurro.tech domains host the azzurrotech client site.
		hostSites["azzurro.tech"] = "azzurrotech"
		hostSites["www.azzurro.tech"] = "azzurrotech"
	}

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
  --host-site   serve a client's hosted site at / on a host (host=client,
                repeatable; default azzurro.tech=azzurrotech)
  --platform-path path on mapped hosts that redirects to the platform
                (default /platform)
  --trust-proxy trust X-Forwarded-For for rate-limit keys — only behind a
                proxy you control (default off; or $STENELLA_TRUST_PROXY=1)
  --allow-private-fetch let server-side feed/OPML fetches reach loopback and
                private addresses (SSRF guard off) — dev/test only
                (default off; or $STENELLA_ALLOW_PRIVATE_FETCH=1)

Routes atp owns: /api, /clients, /c, /gw, /login, /logout, /health.
Routes stenella owns: / (homepage), /s/portal, /s/admin, /s/feed/**,
/s/x/**, /s/api/**, /s/static/**, /s/data/**.
On hosts mapped with --host-site: "/" serves the client's hosted site and
{--platform-path} redirects to the stenella portal for that client.
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
	if *adminPass == "" {
		// Loud on purpose: neither --admin-pass nor $STENELLA_ADMIN_PASSWORD was
		// set, so web.New falls back to the built-in default password "admin".
		// The default stays (tests and fresh dev setups rely on it), but an
		// instance reachable with a public default password must say so before
		// it starts serving. Name the exact remediation so nobody has to guess.
		log.Print("**************************************************************")
		log.Print("* WARNING: no administrator password is configured.         *")
		log.Print("*   stenella is starting with the built-in default          *")
		log.Print("*   password \"admin\". Anyone who can reach this instance     *")
		log.Print("*   can sign in as the administrator. Set a password:        *")
		log.Print("*     --admin-pass=<password>                                *")
		log.Print("*   or                                                       *")
		log.Print("*     STENELLA_ADMIN_PASSWORD=<password>                     *")
		log.Print("**************************************************************")
	}

	// The SSRF guard (netguard) blocks loopback/private/link-local fetch
	// targets by default; this flag is the single documented way to lift it,
	// and it must never be lifted on an internet-facing instance.
	netguard.AllowPrivate = *allowPrivateFetch

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
		SiteHosts:     hostSites,
		PlatformPath:  *platformPath,
		TrustProxy:    *trustProxy,
	})
	if err != nil {
		log.Fatalf("stenella: %v", err)
	}

	go svc.Background(context.Background())

	addr := ":" + *port
	log.Printf("stenella listening on %s (data root %q)", addr, *root)
	server := &http.Server{
		Addr:              addr,
		Handler:           svc.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
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
