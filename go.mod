module azzurrotech/stenella

go 1.22

// stenella is the AzzurroTech data platform. It embeds atp (the orchestrator)
// in-process and uses it as middleware: every feature of atp, song, pod and
// shepherd is exposed through atp's own HTTP surface, never re-implemented.
// The only modules involved are our own — all standard-library-only.
//
// atp in turn embeds pod, shepherd and song from its submodule worktrees, so
// those replaces are mirrored here: Go only honours replace directives from
// the main module, and atp's own go.mod is a dependency, not the main module.
require (
	azzurrotech/atp v0.0.0
	azzurrotech/pod v0.0.0 // indirect
	azzurrotech/shepherd v0.0.0 // indirect
	azzurrotech/song v0.0.0 // indirect
)

replace (
	azzurrotech/atp => ./atp
	azzurrotech/pod => ./atp/pod
	azzurrotech/shepherd => ./atp/shepherd
	azzurrotech/song => ./atp/song
)
