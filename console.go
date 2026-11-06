// console.go — the easy-to-use interface over stenella and the platform.
//
// stenella runs as its own service now, so the console reports both what
// stenella itself serves (apps, routes, head, header policy) and the platform
// services it reaches over HTTP (atp's vault, pod's tables, song's silos,
// shepherd's capabilities and firewall). Every remote call degrades to an empty
// panel when the service is unreachable: a missing atp must not take the OSINT
// platform with it.
//
// Two rules hold here:
//
//   - No secret value is ever read. The vault panel lists names, notes and
//     timestamps only; the value stays server-side, exactly as atp promises.
//   - Every panel reports what is actually mounted. A sub-project with no
//     upstream configured reports shepherd's own "no upstream" answer rather
//     than a cheerful guess.
package main

import (
	"net/http"
	"runtime/debug"
	"sort"
	"strings"
)

// ConsolePlatform describes the running server.
type ConsolePlatform struct {
	Name       string `json:"name"`
	App        string `json:"app"`
	ATPVersion string `json:"atpVersion"`
	Addr       string `json:"addr"`
	Root       string `json:"root"`
	Encrypted  bool   `json:"encrypted"`
}

// ConsoleSubProject is one sub-project of atp as it was actually linked in.
type ConsoleSubProject struct {
	Module  string   `json:"module"`
	Version string   `json:"version"`
	Path    string   `json:"path,omitempty"` // replace target when linked locally
	Local   bool     `json:"local"`
	Mounts  []string `json:"mounts"`
	Purpose string   `json:"purpose"`
}

type ConsoleSecrets struct {
	Apps    []string                `json:"apps"`
	Listing map[string][]SecretInfo `json:"listing"`
}

type ConsoleHead struct {
	Title  string              `json:"title"`
	Global []string            `json:"global"`
	PerApp map[string][]string `json:"perApp"`
}

type ConsoleSong struct {
	Root       string   `json:"root"`
	Silos      []string `json:"silos"`
	Components []string `json:"components"`
}

type ConsolePod struct {
	Tables []TableInfo `json:"tables"`
}

type ConsoleShepherd struct {
	Issuer       string   `json:"issuer"`
	Gateway      string   `json:"gateway"`
	Capabilities []string `json:"capabilities"`
}

type ConsoleFirewall struct {
	DefaultAllow bool             `json:"defaultAllow"`
	Rules        []map[string]any `json:"rules"`
}

type Console struct {
	Platform    ConsolePlatform     `json:"platform"`
	SubProjects []ConsoleSubProject `json:"subprojects"`
	Apps        []AppMeta           `json:"apps"`
	Functions   []Route             `json:"functions"`
	Secrets     ConsoleSecrets      `json:"secrets"`
	Head        ConsoleHead         `json:"head"`
	Headers     []HeaderRule        `json:"headers"`
	Song        ConsoleSong         `json:"song"`
	Pod         ConsolePod          `json:"pod"`
	Shepherd    ConsoleShepherd     `json:"shepherd"`
	Firewall    ConsoleFirewall     `json:"firewall"`
	Static      []string            `json:"static"`
	Counts      map[string]int      `json:"counts"`
}

// console builds the whole console document.
func (a *App) console() Console {
	srv := a.srv
	version := "(unavailable)"
	if st, err := srv.ATP().Platform(); err == nil && st.Version != "" {
		version = st.Version
	}
	fwAllow, fwRules, _ := srv.Shepherd().Firewall()

	return Console{
		Platform: ConsolePlatform{
			Name:       "atp",
			App:        "stenella",
			ATPVersion: version,
			Addr:       srv.Addr(),
			Root:       srv.Root(),
			Encrypted:  srv.Encrypted(),
		},
		SubProjects: subProjects(),
		Apps:        nonNilApps(srv.Apps().List()),
		Functions:   nonNilRoutes(srv.Routes()),
		Secrets:     listSecrets(srv),
		Head:        listHead(srv),
		Headers:     nonNilRules(srv.Headers().Rules()),
		Song:        listSong(srv),
		Pod:         listPod(srv),
		Shepherd:    listShepherd(srv),
		Firewall: ConsoleFirewall{
			DefaultAllow: fwAllow,
			Rules:        nonNilFirewall(fwRules),
		},
		Static: listStatic(srv),
		Counts: a.store.Counts(),
	}
}

// consoleAll answers the whole console document; the front end fetches it in
// one request and renders every panel.
func (a *App) consoleAll(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, a.console())
}

// consoleSurface answers one panel. It exists so a view can refresh a single
// panel without re-reading everything.
func (a *App) consoleSurface(w http.ResponseWriter, r *http.Request) {
	surface := PathParam(r, "surface")
	if surface == "" {
		a.consoleAll(w, r)
		return
	}
	c := a.console()
	switch strings.ToLower(surface) {
	case "platform":
		writeJSON(w, http.StatusOK, c.Platform)
	case "subprojects", "sub-projects":
		writeJSON(w, http.StatusOK, map[string]any{"subprojects": c.SubProjects})
	case "apps":
		writeJSON(w, http.StatusOK, map[string]any{"apps": c.Apps})
	case "functions", "registry":
		writeJSON(w, http.StatusOK, map[string]any{"functions": c.Functions})
	case "secrets", "vault":
		writeJSON(w, http.StatusOK, c.Secrets)
	case "head":
		writeJSON(w, http.StatusOK, c.Head)
	case "headers":
		writeJSON(w, http.StatusOK, map[string]any{"headers": c.Headers})
	case "song":
		writeJSON(w, http.StatusOK, c.Song)
	case "pod":
		writeJSON(w, http.StatusOK, c.Pod)
	case "shepherd":
		writeJSON(w, http.StatusOK, c.Shepherd)
	case "firewall":
		writeJSON(w, http.StatusOK, c.Firewall)
	case "static":
		writeJSON(w, http.StatusOK, map[string]any{"static": c.Static})
	case "counts":
		writeJSON(w, http.StatusOK, c.Counts)
	default:
		writeErr(w, http.StatusNotFound, "unknown console surface "+surface)
	}
}

// ---------------------------------------------------------------------------
// the sub-projects, as actually linked
// ---------------------------------------------------------------------------

// subProjectMeta is what each sub-project does for stenella, from the README.
var subProjectMeta = map[string]struct {
	mounts  []string
	purpose string
}{
	"azzurrotech/song": {
		mounts:  []string{"/", "/{app}/", "/_song/"},
		purpose: "static hosting, server-side templates and head/Web-Component injection",
	},
	"azzurrotech/pod": {
		mounts:  []string{"/pod/"},
		purpose: "the record store behind the information model (one XML file per record)",
	},
	"azzurrotech/shepherd": {
		mounts:  []string{"/gw/", "/api/upstreams", "/api/ratelimit/", "firewall"},
		purpose: "identity-free capability tokens, the gateway, rate limiting and the firewall",
	},
	"azzurrotech/atp": {
		mounts:  []string{"/", "/api/functions", "/secrets", "/head", "/headers", "/_atp/static/"},
		purpose: "the front door: functions, vault, head/header management, apps, static box",
	},
}

// subProjects reports how atp and its three sub-projects are linked into this
// binary. The build info is read from the running executable, so the panel
// shows the real answer — a locally-linked sub-project is reported as local,
// with the path it was replaced by.
func subProjects() []ConsoleSubProject {
	order := []string{"azzurrotech/atp", "azzurrotech/pod", "azzurrotech/song", "azzurrotech/shepherd"}
	found := map[string]ConsoleSubProject{}

	if bi, ok := debug.ReadBuildInfo(); ok {
		for _, dep := range bi.Deps {
			meta, known := subProjectMeta[dep.Path]
			if !known {
				continue
			}
			sp := ConsoleSubProject{
				Module:  dep.Path,
				Version: dep.Version,
				Mounts:  meta.mounts,
				Purpose: meta.purpose,
			}
			if dep.Replace != nil {
				sp.Path = dep.Replace.Path
				sp.Version = dep.Replace.Version
				sp.Local = strings.HasPrefix(dep.Replace.Path, ".") ||
					strings.HasPrefix(dep.Replace.Path, "/")
			}
			found[dep.Path] = sp
		}
	}

	out := make([]ConsoleSubProject, 0, len(order))
	for _, p := range order {
		if sp, ok := found[p]; ok {
			out = append(out, sp)
			continue
		}
		// fall back to the static description when build info is unavailable
		meta := subProjectMeta[p]
		out = append(out, ConsoleSubProject{
			Module:  p,
			Version: "(remote)",
			Mounts:  meta.mounts,
			Purpose: meta.purpose,
		})
	}
	return out
}

// ---------------------------------------------------------------------------
// panel readers
// ---------------------------------------------------------------------------

func listSecrets(srv *Server) ConsoleSecrets {
	apps, listing, err := srv.ATP().Secrets()
	if err != nil {
		return ConsoleSecrets{Apps: []string{}, Listing: map[string][]SecretInfo{}}
	}
	sort.Strings(apps)
	if listing == nil {
		listing = map[string][]SecretInfo{}
	}
	for app, infos := range listing {
		listing[app] = nonNilSecretInfos(infos)
	}
	return ConsoleSecrets{Apps: nonNilStrings(apps), Listing: listing}
}

func listHead(srv *Server) ConsoleHead {
	h := srv.Head()
	apps := nonNilStrings(srv.Apps().Names())
	perApp := map[string][]string{}
	for _, app := range apps {
		if frags := h.FragmentsFor(app); len(frags) > 0 {
			perApp[app] = frags
		}
	}
	return ConsoleHead{
		Title:  h.TitleValue(""),
		Global: nonNilStrings(h.GlobalFragments()),
		PerApp: perApp,
	}
}

func listSong(srv *Server) ConsoleSong {
	root, silos, err := srv.Song().Silos()
	out := ConsoleSong{}
	if err == nil {
		out.Root = root
		out.Silos = nonNilStrings(silos)
	}
	if comps, err := srv.Song().Components(); err == nil {
		out.Components = nonNilStrings(comps)
	} else {
		out.Components = []string{}
	}
	return out
}

func listPod(srv *Server) ConsolePod {
	tables, err := srv.Pod().Tables()
	if err != nil {
		return ConsolePod{Tables: []TableInfo{}}
	}
	out := make([]TableInfo, 0, len(tables))
	for _, t := range tables {
		out = append(out, t)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return ConsolePod{Tables: out}
}

func listShepherd(srv *Server) ConsoleShepherd {
	return ConsoleShepherd{
		Issuer:  srv.Shepherd().Issuer(),
		Gateway: "/gw/",
		Capabilities: []string{
			"capability tokens (issue at /api/keys, verify at /api/token/verify, revoke at /api/revoke)",
			"the /gw/ gateway with upstreams (/api/upstreams)",
			"rate limiting (/api/ratelimit/status)",
			"the request firewall",
			"magic links and JavaScript Web Crypto keys (/api/keys/magic, /api/keys/javascript)",
		},
	}
}

func listStatic(srv *Server) []string {
	names, err := srv.ATP().Static()
	if err != nil {
		return []string{}
	}
	return nonNilStrings(names)
}

// ---------------------------------------------------------------------------
// nil-safe renderers: a JSON panel should be [] not null
// ---------------------------------------------------------------------------

func nonNilStrings(in []string) []string {
	if in == nil {
		return []string{}
	}
	return in
}

func nonNilApps(in []AppMeta) []AppMeta {
	if in == nil {
		return []AppMeta{}
	}
	return in
}

func nonNilRoutes(in []Route) []Route {
	if in == nil {
		return []Route{}
	}
	return in
}

func nonNilRules(in []HeaderRule) []HeaderRule {
	if in == nil {
		return []HeaderRule{}
	}
	return in
}

func nonNilFirewall(in []map[string]any) []map[string]any {
	if in == nil {
		return []map[string]any{}
	}
	return in
}

func nonNilSecretInfos(in []SecretInfo) []SecretInfo {
	if in == nil {
		return []SecretInfo{}
	}
	return in
}
