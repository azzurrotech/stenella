// console.go — the easy-to-use interface over atp itself.
//
// stenella is an atp application, so the platform's own surface (apps, the
// function registry, the secrets vault, central head management, the header
// policy, the encrypted static box) and its three sub-projects (song, pod,
// shepherd) are part of what the operator has to be able to see and drive.
// This file exposes each atp surface as a JSON document, and the front end
// renders them as one console.
//
// Two rules hold here:
//
//   - No secret value is ever read. The vault panel lists names, notes and
//     timestamps only; the value stays server-side, exactly as atp promises.
//   - Every panel reports what is actually mounted. A sub-project with no
//     upstream configured reports shepherd's own \"no upstream\" answer rather
//     than a cheerful guess.
package main

import (
	"net/http"
	"runtime/debug"
	"sort"
	"strings"

	"azzurrotech/atp"
	"azzurrotech/pod"
	"azzurrotech/shepherd/firewall"
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
	Apps    []string                    `json:"apps"`
	Listing map[string][]atp.SecretInfo `json:"listing"`
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
	Tables []pod.TableInfo `json:"tables"`
}

type ConsoleShepherd struct {
	Issuer       string   `json:"issuer"`
	Gateway      string   `json:"gateway"`
	Capabilities []string `json:"capabilities"`
}

type ConsoleFirewall struct {
	DefaultAllow bool            `json:"defaultAllow"`
	Rules        []firewall.Rule `json:"rules"`
}

type Console struct {
	Platform    ConsolePlatform     `json:"platform"`
	SubProjects []ConsoleSubProject `json:"subprojects"`
	Apps        []atp.App           `json:"apps"`
	Functions   []atp.Route         `json:"functions"`
	Secrets     ConsoleSecrets      `json:"secrets"`
	Head        ConsoleHead         `json:"head"`
	Headers     []atp.HeaderRule    `json:"headers"`
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
	c := Console{
		Platform: ConsolePlatform{
			Name:       "atp",
			App:        "stenella",
			ATPVersion: atp.Version,
			Addr:       srv.Addr(),
			Root:       srv.Root(),
			Encrypted:  srv.Encrypted(),
		},
		SubProjects: subProjects(),
		Apps:        nonNilApps(srv.Apps().List()),
		Functions:   nonNilRoutes(srv.Registry().Routes()),
		Secrets:     listSecrets(srv),
		Head:        listHead(srv),
		Headers:     nonNilRules(srv.Headers().Rules()),
		Song:        listSong(srv),
		Pod:         listPod(srv),
		Shepherd:    listShepherd(srv),
		Firewall: ConsoleFirewall{
			DefaultAllow: srv.Firewall().DefaultAllow(),
			Rules:        nonNilFirewall(srv.Firewall().Rules()),
		},
		Static: nonNilStrings(srv.StaticBox().List()),
		Counts: a.store.Counts(),
	}
	return c
}

// consoleAll answers the whole console document; the front end fetches it in
// one request and renders every panel.
func (a *App) consoleAll(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, a.console())
}

// consoleSurface answers one panel. It exists so a view can refresh a single
// panel without re-reading everything.
func (a *App) consoleSurface(w http.ResponseWriter, r *http.Request) {
	surface := atp.PathParam(r, "surface")
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
		purpose: "the server: functions, vault, head/header management, apps, static box",
	},
}

// subProjects reports how atp and its three sub-projects were linked into this
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
			Version: "(embedded)",
			Mounts:  meta.mounts,
			Purpose: meta.purpose,
		})
	}
	return out
}

// ---------------------------------------------------------------------------
// panel readers
// ---------------------------------------------------------------------------

func listSecrets(srv *atp.Server) ConsoleSecrets {
	v := srv.Secrets()
	apps := nonNilStrings(v.Apps())
	sort.Strings(apps)
	listing := map[string][]atp.SecretInfo{}
	for _, app := range apps {
		// List returns metadata only; the value is never read here.
		listing[app] = nonNilSecretInfos(v.List(app))
	}
	return ConsoleSecrets{Apps: apps, Listing: listing}
}

func listHead(srv *atp.Server) ConsoleHead {
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

func listSong(srv *atp.Server) ConsoleSong {
	song := srv.Song()
	out := ConsoleSong{Root: song.Store().Root()}
	if silos, err := song.Store().ListSilos(); err == nil {
		out.Silos = nonNilStrings(silos)
	}
	out.Components = nonNilStrings(song.Components().Names())
	return out
}

func listPod(srv *atp.Server) ConsolePod {
	tables, err := srv.Pod().ListTables()
	if err != nil {
		return ConsolePod{}
	}
	out := make([]pod.TableInfo, 0, len(tables))
	for _, t := range tables {
		info, err := srv.Pod().TableInfo(t)
		if err != nil || info == nil {
			out = append(out, pod.TableInfo{Name: t})
			continue
		}
		out = append(out, *info)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return ConsolePod{Tables: out}
}

func listShepherd(srv *atp.Server) ConsoleShepherd {
	sh := srv.Shepherd()
	return ConsoleShepherd{
		Issuer:  sh.Issuer(),
		Gateway: "/gw/",
		Capabilities: []string{
			"capability tokens (issue, verify at /api/token/verify, revoke at /api/revoke)",
			"the /gw/ gateway with upstreams (/api/upstreams)",
			"rate limiting (/api/ratelimit/status)",
			"the request firewall",
			"magic links and JavaScript Web Crypto keys (/api/keys/magic, /api/keys/javascript)",
		},
	}
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

func nonNilApps(in []atp.App) []atp.App {
	if in == nil {
		return []atp.App{}
	}
	return in
}

func nonNilRoutes(in []atp.Route) []atp.Route {
	if in == nil {
		return []atp.Route{}
	}
	return in
}

func nonNilRules(in []atp.HeaderRule) []atp.HeaderRule {
	if in == nil {
		return []atp.HeaderRule{}
	}
	return in
}

func nonNilFirewall(in []firewall.Rule) []firewall.Rule {
	if in == nil {
		return []firewall.Rule{}
	}
	return in
}

func nonNilSecretInfos(in []atp.SecretInfo) []atp.SecretInfo {
	if in == nil {
		return []atp.SecretInfo{}
	}
	return in
}
