// model.go — stenella's information model.
//
// The README fixes the record set:
//
//	source · item · entity · link · comment · note · group · rule · insight
//
// Every one of them is stored as a pod record (one plain XML file per row under
// the pod store), which is what makes the model inspectable and portable: the
// records are the platform, and anything that can read XML can read them.
//
// One deliberate refinement of the README's normalised item shape: the source's
// `link` and `guid` are reduced to hashes at collection time. The README
// requires both that de-duplication collapse a story "by stable identity (guid,
// then canonical link, then a content hash)" and that nothing stenella presents
// carries a link. Keeping the identity as a hash satisfies both at once, and
// means the promise holds even through pod's own record endpoint, not just
// through the sanitiser. The same reasoning drops the author: it is a private
// element, so only its presence is recorded in prose, never its value.
package main

import "time"

// Source is one configured collection point: a URL plus a kind and a schedule.
//
// The URL is operator configuration. It is used server-side, is visible in the
// operator console, and is never echoed into anything stenella shares.
type Source struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	URL         string    `json:"url"`
	Kind        string    `json:"kind"`          // feed | page | dataset
	Collection  string    `json:"collection"`    // the tasking this source serves
	Reliability string    `json:"reliability"`   // A–F, see grading below
	ACL         string    `json:"acl,omitempty"` // public | private | protected
	Enabled     bool      `json:"enabled"`
	Every       string    `json:"every,omitempty"` // fetch cadence, e.g. "15m"
	Added       time.Time `json:"added,omitempty"`
	LastFetch   time.Time `json:"lastFetch,omitempty"`
	LastStatus  string    `json:"lastStatus,omitempty"`
	LastCount   int       `json:"lastCount,omitempty"`
}

// The three source kinds the README fixes. A feed and a page are fetched; a
// dataset is imported by the analyst, so it is never fetched and never reported
// as a collection failure.
const (
	sourceKindFeed    = "feed"
	sourceKindPage    = "page"
	sourceKindDataset = "dataset"
)

// isSourceKind reports whether k is one of the kinds an operator may configure.
func isSourceKind(k string) bool {
	switch k {
	case sourceKindFeed, sourceKindPage, sourceKindDataset:
		return true
	}
	return false
}

// Reliability grades follow the Admiralty scale the discipline uses: A/B are
// reliable, C/D usually/once reliable, E/F unreliable, and the digit is the
// information's own credibility (1 confirmed … 6 cannot be judged).
var reliabilityGrades = []string{"A", "B", "C", "D", "E", "F"}

// Item is one collected, normalised entry — the atom of the platform.
//
// Title, Summary, Body and Categories are already plain link-free prose:
// sanitising happens at collection, so no code path can accidentally render a
// raw source document. LinkHash/GUIDHash/ContentHash are the de-duplication
// identity and are one-way.
type Item struct {
	ID           string    `json:"id"`
	SourceID     string    `json:"sourceId"`
	Source       string    `json:"source"` // source name, the only provenance shown
	Collection   string    `json:"collection"`
	Title        string    `json:"title"`
	Summary      string    `json:"summary,omitempty"`
	Body         string    `json:"body,omitempty"`
	Categories   []string  `json:"categories,omitempty"`
	ACL          string    `json:"acl,omitempty"` // public | private | protected
	LinkHash     string    `json:"linkHash,omitempty"`
	GUIDHash     string    `json:"guidHash,omitempty"`
	ContentHash  string    `json:"contentHash,omitempty"`
	Attributions []string  `json:"attributions,omitempty"` // every source that carried the story
	Entities     []string  `json:"entities,omitempty"`     // entity ids
	Geo          string    `json:"geo,omitempty"`          // "lat,lon" when the item carries coordinates
	Published    time.Time `json:"published,omitempty"`
	Fetched      time.Time `json:"fetched,omitempty"`
	DupOf        string    `json:"dupOf,omitempty"` // the item this one collapsed into
}

// Entity is a person, organisation, place, handle or keyword extracted from
// items. Entities are what links between items are built from.
type Entity struct {
	ID        string    `json:"id"`
	Kind      string    `json:"kind"` // person | org | place | handle | keyword
	Name      string    `json:"name"`
	Count     int       `json:"count"`
	Items     []string  `json:"items,omitempty"`
	FirstSeen time.Time `json:"firstSeen,omitempty"`
	LastSeen  time.Time `json:"lastSeen,omitempty"`
}

// Link is a typed, directed edge between two items, or between an item and a
// name the analyst chose. Origin records whether the platform proposed it or an
// analyst asserted it; only human links are treated as analysis.
type Link struct {
	ID       string    `json:"id"`
	From     string    `json:"from"`
	To       string    `json:"to"`
	Relation string    `json:"relation"` // supports | contradicts | duplicates | references | related
	Origin   string    `json:"origin"`   // auto | human
	Note     string    `json:"note,omitempty"`
	Created  time.Time `json:"created,omitempty"`
}

// Comment is an analyst's annotation on an item. Author is the analyst, not a
// source, so it is the operator's own attribution and is kept.
type Comment struct {
	ID      string    `json:"id"`
	Item    string    `json:"item"`
	Author  string    `json:"author"`
	Body    string    `json:"body"`
	Created time.Time `json:"created,omitempty"`
}

// Note is a free-standing analyst record, optionally attached to an item or an
// entity. Insights are promoted into notes.
type Note struct {
	ID      string    `json:"id"`
	Subject string    `json:"subject,omitempty"` // item or entity id
	Title   string    `json:"title"`
	Body    string    `json:"body"`
	Tags    []string  `json:"tags,omitempty"`
	Created time.Time `json:"created,omitempty"`
	Updated time.Time `json:"updated,omitempty"`
}

// Group is a saved query or watchlist: the durable form of "what I am tracking".
type Group struct {
	ID      string    `json:"id"`
	Name    string    `json:"name"`
	Query   string    `json:"query,omitempty"` // a metadata query, e.g. "tag:critical source:CBC"
	Tags    []string  `json:"tags,omitempty"`
	Color   string    `json:"color,omitempty"`
	Created time.Time `json:"created,omitempty"`
}

// Rule is one declarative insight rule. Its conditions are data, not code, so a
// rule can be created, edited and disabled at runtime and re-run over the whole
// collection.
type Rule struct {
	ID         string    `json:"id"`
	Name       string    `json:"name"`
	Text       string    `json:"text"`     // the human sentence the insight states
	Severity   string    `json:"severity"` // info | low | medium | high | critical
	Window     string    `json:"window,omitempty"`
	Enabled    bool      `json:"enabled"`
	Tags       []string  `json:"tags,omitempty"`
	Sources    []string  `json:"sources,omitempty"`
	Entities   []string  `json:"entities,omitempty"`
	MatchAll   bool      `json:"matchAll,omitempty"`   // every tag must match, not any
	MinItems   int       `json:"minItems,omitempty"`   // fire only with this many matching items
	MinSources int       `json:"minSources,omitempty"` // …and this many distinct sources
	Created    time.Time `json:"created,omitempty"`
}

// Insight is the output of a rule: a claim with its citations. An insight is
// never deleted silently — it is dismissed with a reason, or promoted to a note.
type Insight struct {
	ID       string    `json:"id"`
	Rule     string    `json:"rule"`
	Title    string    `json:"title"`
	Text     string    `json:"text"`
	Severity string    `json:"severity"`
	Items    []string  `json:"items,omitempty"` // citations: the items that fired the rule
	Sources  []string  `json:"sources,omitempty"`
	Count    int       `json:"count"`
	Status   string    `json:"status"` // new | dismissed | promoted
	Reason   string    `json:"reason,omitempty"`
	NoteID   string    `json:"noteId,omitempty"`
	Created  time.Time `json:"created,omitempty"`
	Updated  time.Time `json:"updated,omitempty"`
}
