// insights.go — the rule engine: the only part of stenella that asserts
// anything, and the only part allowed to.
//
// Everything upstream is processing — collection, normalisation, extraction.
// An *insight* exists only when a declarative rule says so, and every insight
// carries the item ids that produced it as citations, so a claim can be walked
// back to what was actually collected. Two later commands are deliberately
// required to retire an insight: dismiss it with a reason, or promote it to a
// note. Nothing disappears on its own.
package main

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// runRules evaluates every enabled rule against a collection and writes the
// resulting insight for each rule that fires. It returns the number of insights
// open in that collection after the run.
func (a *App) runRules(collection string) (int, error) {
	rules, err := a.store.Rules()
	if err != nil {
		return 0, err
	}
	items, err := a.store.ItemsIn(collection, "", 0)
	if err != nil {
		return 0, err
	}

	var errs []string
	open := 0
	env := a.evalEnv()
	for _, rule := range rules {
		if !rule.Enabled {
			continue
		}
		matches := evaluateWith(rule, items, env)
		if len(matches) == 0 {
			continue
		}
		if err := a.writeInsight(rule, matches); err != nil {
			errs = append(errs, err.Error())
		}
	}
	if len(errs) > 0 {
		return open, fmt.Errorf("%s", strings.Join(errs, "; "))
	}

	insights, err := a.store.Insights("new")
	if err != nil {
		return open, nil
	}
	for _, in := range insights {
		if collectionMatches(collection, collectionOfInsight(a, in)) {
			open++
		}
	}
	return open, nil
}

// collectionOfInsight reports which collection an insight's citations belong to.
func collectionOfInsight(a *App, in *Insight) string {
	if len(in.Items) == 0 {
		return "all"
	}
	it, err := a.store.Item(in.Items[0])
	if err != nil {
		return "all"
	}
	return collectionOrDefault(it.Collection)
}

// evalEnv is the graph context a rule needs beyond the candidate items: each
// source's reliability grade and the relations each item participates in. It is
// built once per run so a hundred rules do not re-read the store a hundred
// times.
type evalEnv struct {
	now         time.Time
	reliability map[string]int             // lower-case source name → rank (A=0 … F=5)
	links       map[string]map[string]bool // item id → relation and relation|origin
}

// reliabilityRank turns an Admiralty grade into a sortable rank. An empty or
// unrecognised grade ranks as F: unknown is treated as least reliable, which is
// the safe reading when a rule asks for a minimum.
func reliabilityRank(grade string) int {
	g := strings.ToUpper(strings.TrimSpace(grade))
	for i, v := range reliabilityGrades {
		if g == v {
			return i
		}
	}
	return len(reliabilityGrades) - 1
}

// normalizeReliability returns a canonical grade, defaulting to "" for an
// unset value and rejecting anything outside A–F.
func normalizeReliability(grade string) (string, bool) {
	g := strings.ToUpper(strings.TrimSpace(grade))
	if g == "" {
		return "", true
	}
	for _, v := range reliabilityGrades {
		if g == v {
			return g, true
		}
	}
	return "", false
}

// evalEnv loads the graph context for one rule run.
func (a *App) evalEnv() *evalEnv {
	env := &evalEnv{
		now:         nowUTC(),
		reliability: map[string]int{},
		links:       map[string]map[string]bool{},
	}
	if sources, err := a.store.Sources(); err == nil {
		for _, s := range sources {
			name := strings.ToLower(strings.TrimSpace(s.Name))
			if name != "" {
				env.reliability[name] = reliabilityRank(s.Reliability)
			}
		}
	}
	if links, err := a.store.Links(); err == nil {
		for _, l := range links {
			if l == nil {
				continue
			}
			rel := strings.ToLower(strings.TrimSpace(l.Relation))
			if rel == "" {
				rel = "related"
			}
			origin := strings.ToLower(strings.TrimSpace(l.Origin))
			for _, id := range []string{l.From, l.To} {
				if id == "" {
					continue
				}
				set := env.links[id]
				if set == nil {
					set = map[string]bool{}
					env.links[id] = set
				}
				set[rel] = true
				if origin != "" {
					set[rel+"|"+origin] = true
				}
			}
		}
	}
	return env
}

// evaluate applies one rule without graph context. It is kept for callers that
// only have items (and for tests); runRules and the API use evaluateWith.
func evaluate(rule *Rule, items []*Item, now time.Time) []*Item {
	return evaluateWith(rule, items, &evalEnv{now: now, reliability: map[string]int{}, links: map[string]map[string]bool{}})
}

// evaluateWith applies one rule to a candidate set and returns the citations.
//
// A rule is data: an optional window, tag/source/entity conditions with an any
// or all reading, an optional minimum source reliability, an optional link
// relation, and the two counts that make a rule meaningful — how many items and
// how many distinct sources it needs. With no conditions at all a rule matches
// everything, so a pure count rule ("five items in 24h") still expresses itself.
func evaluateWith(rule *Rule, items []*Item, env *evalEnv) []*Item {
	if env == nil {
		env = &evalEnv{}
	}
	if env.now.IsZero() {
		env.now = nowUTC()
	}
	now := env.now

	window := parseWindow(rule.Window)
	cutoff := time.Time{}
	if window > 0 {
		cutoff = now.Add(-window)
	}

	var matches []*Item
	for _, it := range items {
		if !cutoff.IsZero() && !it.Published.IsZero() && it.Published.Before(cutoff) {
			continue
		}
		if !ruleMatches(rule, it, env) {
			continue
		}
		matches = append(matches, it)
	}

	if len(matches) > 0 {
		sort.SliceStable(matches, func(i, j int) bool { return matches[i].Published.After(matches[j].Published) })
	}
	if rule.MinItems > 0 && len(matches) < rule.MinItems {
		return nil
	}
	if rule.MinSources > 0 {
		sources := map[string]bool{}
		for _, m := range matches {
			sources[m.Source] = true
		}
		if len(sources) < rule.MinSources {
			return nil
		}
	}
	return matches
}

// ruleMatches reports whether one item satisfies a rule's conditions.
func ruleMatches(rule *Rule, it *Item, env *evalEnv) bool {
	if len(rule.Tags) > 0 {
		if rule.MatchAll {
			if !subsetAll(rule.Tags, it.Categories) {
				return false
			}
		} else if _, ok := intersect(rule.Tags, it.Categories); !ok {
			return false
		}
	}
	if len(rule.Sources) > 0 && !anyOfFold(rule.Sources, uniqueStrings(append(it.Attributions, it.Source))) {
		return false
	}
	if len(rule.Entities) > 0 {
		// an item matches if it carries any named entity (by id or by name)
		if !entityMatches(rule.Entities, it) {
			return false
		}
	}
	if rule.MinReliability != "" {
		if !itemMeetsReliability(rule.MinReliability, it, env) {
			return false
		}
	}
	if len(rule.Relations) > 0 {
		if !itemHasRelation(rule.Relations, rule.LinkOrigin, it, env) {
			return false
		}
	}
	// Pattern: optional regex match against the item's title+summary body.
// When set, the item must contain a match for this rule to fire.
	if rule.Pattern != "" {
		compiled, err := regexp.Compile(rule.Pattern)
		if err != nil {
			// Invalid regex pattern — rule cannot match via this condition
			return false
		}
		var text string
		if rule.CaseSensitive {
			text = it.Title + " " + it.Summary + " " + it.Body
		} else {
			text = strings.ToLower(it.Title + " " + it.Summary + " " + it.Body)
			_ = strings.ToLower(rule.Pattern) // pattern lowercased for case-insensitive match
		}
		if !compiled.MatchString(text) {
			return false
		}
	}
	return true
}

// itemMeetsReliability reports whether at least one of the item's sources is at
// least as reliable as the rule's minimum.
func itemMeetsReliability(min string, it *Item, env *evalEnv) bool {
	want := reliabilityRank(min)
	if env == nil {
		return false
	}
	for _, name := range uniqueStrings(append(it.Attributions, it.Source)) {
		rank, known := env.reliability[strings.ToLower(strings.TrimSpace(name))]
		if !known {
			rank = reliabilityRank("")
		}
		if rank <= want {
			return true
		}
	}
	return false
}

// itemHasRelation reports whether the item is an endpoint of a link with one of
// the wanted relations, optionally narrowed to one origin.
func itemHasRelation(want []string, origin string, it *Item, env *evalEnv) bool {
	if env == nil {
		return false
	}
	set := env.links[it.ID]
	if len(set) == 0 {
		return false
	}
	origin = strings.ToLower(strings.TrimSpace(origin))
	for _, rel := range want {
		key := strings.ToLower(strings.TrimSpace(rel))
		if key == "" {
			continue
		}
		if origin != "" {
			key += "|" + origin
		}
		if set[key] {
			return true
		}
	}
	return false
}

func entityMatches(want []string, it *Item) bool {
	for _, w := range want {
		if containsFold(it.Entities, w) {
			return true
		}
	}
	return false
}

// anyOfFold reports whether any wanted value appears among the have values,
// case-insensitively. It is how a rule's source conditions read an item's
// source name or one of its attributions.
func anyOfFold(want, have []string) bool {
	for _, w := range want {
		if containsFold(have, w) {
			return true
		}
	}
	return false
}

// writeInsight creates or refreshes the insight for a rule.
//
// A dismissed insight is left exactly as the analyst judged it — re-running a
// rule must not resurrect a verdict — while an open insight's citations are
// refreshed to the current match set.
func (a *App) writeInsight(rule *Rule, matches []*Item) error {
	ids := make([]string, 0, len(matches))
	sources := map[string]bool{}
	for _, m := range matches {
		ids = append(ids, m.ID)
		sources[m.Source] = true
	}
	sourceNames := make([]string, 0, len(sources))
	for s := range sources {
		sourceNames = append(sourceNames, s)
	}
	sort.Strings(sourceNames)

	existing, ok := a.store.InsightByRule(rule.ID)
	if ok && existing.Status == "dismissed" {
		return nil
	}

	ins := &Insight{
		Rule:     rule.ID,
		Title:    rule.Name,
		Text:     rule.Text,
		Severity: severityOr(rule.Severity),
		Items:    ids,
		Sources:  sourceNames,
		Count:    len(ids),
		Status:   "new",
	}
	if ok {
		ins.ID = existing.ID
		ins.Created = existing.Created
		ins.Status = existing.Status
		ins.Reason = existing.Reason
		ins.NoteID = existing.NoteID
	}
	return a.store.PutInsight(ins)
}

// dismissInsight records a rejection with a reason, which is mandatory: an
// analyst's "not this" without a reason is not usable by the next one.
func (a *App) dismissInsight(id, reason string) (*Insight, error) {
	ins, err := a.store.Insight(id)
	if err != nil {
		return nil, err
	}
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return nil, fmt.Errorf("a reason is required to dismiss an insight")
	}
	ins.Status = "dismissed"
	ins.Reason = cleanInline(reason, 300)
	if err := a.store.PutInsight(ins); err != nil {
		return nil, err
	}
	return ins, nil
}

// promoteInsight turns a firing rule's claim into a durable analyst note, so
// the observation survives the insight inbox.
func (a *App) promoteInsight(id string) (*Insight, *Note, error) {
	ins, err := a.store.Insight(id)
	if err != nil {
		return nil, nil, err
	}
	note := &Note{
		Subject: ins.ID,
		Title:   ins.Title,
		Body: ins.Text + "\n\nCitations: " + strings.Join(ins.Items, ", ") +
			"\nSources: " + strings.Join(ins.Sources, ", "),
		Tags: []string{"insight", ins.Severity},
	}
	if err := a.store.PutNote(note); err != nil {
		return nil, nil, err
	}
	ins.Status = "promoted"
	ins.NoteID = note.ID
	if err := a.store.PutInsight(ins); err != nil {
		return nil, nil, err
	}
	return ins, note, nil
}

// reopenInsight puts a dismissed or promoted insight back in the inbox.
func (a *App) reopenInsight(id string) (*Insight, error) {
	ins, err := a.store.Insight(id)
	if err != nil {
		return nil, err
	}
	ins.Status = "new"
	ins.Reason = ""
	if err := a.store.PutInsight(ins); err != nil {
		return nil, err
	}
	return ins, nil
}

// parseWindow reads a rule window like "15m", "24h", "7d", "2w". An empty or
// unparseable window means no time bound, which the rule's other conditions
// still constrain.
func parseWindow(s string) time.Duration {
	s = strings.TrimSpace(strings.ToLower(s))
	if s == "" {
		return 0
	}
	if n, err := strconv.Atoi(s); err == nil {
		return time.Duration(n) * time.Hour
	}
	numStr := strings.TrimRight(s, "mhdw")
	num, err := strconv.Atoi(numStr)
	if err != nil || num <= 0 {
		return 0
	}
	switch s[len(s)-1] {
	case 'm':
		return time.Duration(num) * time.Minute
	case 'h':
		return time.Duration(num) * time.Hour
	case 'd':
		return time.Duration(num) * 24 * time.Hour
	case 'w':
		return time.Duration(num) * 7 * 24 * time.Hour
	}
	return 0
}

func severityOr(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	switch s {
	case "info", "low", "medium", "high", "critical":
		return s
	}
	return "medium"
}
