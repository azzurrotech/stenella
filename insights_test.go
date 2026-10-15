package main

import (
	"net/http"
	"strings"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// STX06 — windows and severity
// ---------------------------------------------------------------------------

func TestParseWindowUnits(t *testing.T) {
	cases := map[string]time.Duration{
		"":     0,
		"15m":  15 * time.Minute,
		"24h":  24 * time.Hour,
		"7d":   7 * 24 * time.Hour,
		"2w":   2 * 7 * 24 * time.Hour,
		"3":    3 * time.Hour, // a bare number is hours
		" 1D ": 24 * time.Hour,
		"0h":   0,
		"-5h":  0,
		"abc":  0,
		"5x":   0,
	}
	for in, want := range cases {
		if got := parseWindow(in); got != want {
			t.Errorf("parseWindow(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestSeverityOrNormalises(t *testing.T) {
	cases := map[string]string{
		"":          "medium",
		"HIGH":      "high",
		" critical": "critical",
		"info":      "info",
		"low":       "low",
		"nonsense":  "medium",
	}
	for in, want := range cases {
		if got := severityOr(in); got != want {
			t.Errorf("severityOr(%q) = %q, want %q", in, got, want)
		}
	}
}

// ---------------------------------------------------------------------------
// STX06 — evaluate: conditions, window, counts
// ---------------------------------------------------------------------------

func evalFixture(now time.Time) []*Item {
	mk := func(id, src string, age time.Duration, cats, entities []string) *Item {
		return &Item{
			ID: id, Title: "item " + id, Source: src, Attributions: []string{src},
			Collection: "news", Categories: cats, Entities: entities,
			Published: now.Add(-age), Fetched: now, ACL: "public",
		}
	}
	return []*Item{
		mk("recent-cyber", "CBC", time.Hour, []string{"cyber"}, []string{"place:abc"}),
		mk("recent-supply", "ANSA", 2*time.Hour, []string{"supply-chain"}, []string{"org:xyz"}),
		mk("old-cyber", "CBC", 72*time.Hour, []string{"cyber"}, nil),
		mk("both", "CBC", 30*time.Minute, []string{"cyber", "supply-chain"},
			[]string{"place:abc", "org:xyz"}),
	}
}

func idsOf(items []*Item) []string {
	out := make([]string, 0, len(items))
	for _, it := range items {
		out = append(out, it.ID)
	}
	return out
}

func TestEvaluateAppliesWindowTagsSourcesEntitiesAndCounts(t *testing.T) {
	now := time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)
	items := evalFixture(now)

	cases := []struct {
		name string
		rule Rule
		want []string
	}{
		{"no conditions matches everything", Rule{}, []string{"both", "recent-cyber", "recent-supply", "old-cyber"}},
		{"window bounds by publication time", Rule{Window: "24h"}, []string{"both", "recent-cyber", "recent-supply"}},
		{"tag matches any", Rule{Tags: []string{"cyber"}}, []string{"both", "recent-cyber", "old-cyber"}},
		{"tag all requires every tag", Rule{Tags: []string{"cyber", "supply-chain"}, MatchAll: true}, []string{"both"}},
		{"source matches by name", Rule{Sources: []string{"ANSA"}}, []string{"recent-supply"}},
		{"source is case-insensitive", Rule{Sources: []string{"ansa"}}, []string{"recent-supply"}},
		{"entity matches by id", Rule{Entities: []string{"place:abc"}}, []string{"both", "recent-cyber"}},
		{"entity that nothing carries", Rule{Entities: []string{"place:none"}}, nil},
		{"MinItems gates a rule", Rule{Tags: []string{"cyber"}, MinItems: 4}, nil},
		{"MinItems satisfied", Rule{Tags: []string{"cyber"}, MinItems: 3}, []string{"both", "recent-cyber", "old-cyber"}},
		{"MinSources gates a rule", Rule{Tags: []string{"cyber"}, MinSources: 2}, nil},
		{"MinSources satisfied across the set", Rule{Window: "24h", MinSources: 2}, []string{"both", "recent-cyber", "recent-supply"}},
		{"conditions combine", Rule{Tags: []string{"cyber"}, Sources: []string{"CBC"}, Window: "24h"},
			[]string{"both", "recent-cyber"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := idsOf(evaluate(&c.rule, items, now))
			if strings.Join(got, ",") != strings.Join(c.want, ",") {
				t.Errorf("evaluate = %v, want %v", got, c.want)
			}
		})
	}

	// a matching set is ordered newest first, so the inbox reads chronologically
	got := evaluate(&Rule{}, items, now)
	if got[0].ID != "both" || got[len(got)-1].ID != "old-cyber" {
		t.Errorf("order = %v, want newest first", idsOf(got))
	}
}

// ---------------------------------------------------------------------------
// STX06 — rules produce cited insights
// ---------------------------------------------------------------------------

// ruleFixture seeds two sources reporting cyber material plus one unrelated
// item, and returns the app and the rule that should fire.
func ruleFixture(t *testing.T) (*App, *Rule) {
	t.Helper()
	a := analyseFixture(t)
	now := time.Now().UTC()

	wireA := putItem(t, a.store, "item-a", "Ransomware report from Ottawa", "CBC", "news",
		now.Add(-time.Hour), []string{"cyber"})
	wireA.Entities = []string{"place:ottawa"}
	if err := a.store.PutItem(wireA); err != nil {
		t.Fatal(err)
	}
	wireB := putItem(t, a.store, "item-b", "Vulnerability disclosure", "ANSA", "news",
		now.Add(-2*time.Hour), []string{"cyber"})
	if err := a.store.PutItem(wireB); err != nil {
		t.Fatal(err)
	}
	putItem(t, a.store, "item-c", "Harvest forecast", "ANSA", "news",
		now.Add(-time.Hour), []string{"economy"})

	rule := &Rule{
		Name: "Corroborated cyber reporting", Text: "Two or more sources report on cyber.",
		Severity: "high", Enabled: true, Window: "24h",
		Tags: []string{"cyber"}, MinItems: 2, MinSources: 2,
	}
	if err := a.store.PutRule(rule); err != nil {
		t.Fatal(err)
	}
	return a, rule
}

func TestRunRulesWritesACitedInsight(t *testing.T) {
	a, rule := ruleFixture(t)

	open, err := a.runRules("all")
	if err != nil {
		t.Fatal(err)
	}
	if open != 1 {
		t.Errorf("open insights = %d, want 1", open)
	}

	insights, err := a.store.Insights("new")
	if err != nil {
		t.Fatal(err)
	}
	if len(insights) != 1 {
		t.Fatalf("insights = %d, want 1", len(insights))
	}
	ins := insights[0]

	if ins.Rule != rule.ID {
		t.Errorf("insight rule = %q, want %q", ins.Rule, rule.ID)
	}
	if ins.Status != "new" {
		t.Errorf("status = %q, want new", ins.Status)
	}
	if ins.Severity != "high" {
		t.Errorf("severity = %q, want high", ins.Severity)
	}
	if ins.Title != rule.Name || ins.Text != rule.Text {
		t.Errorf("insight does not carry the rule's claim: %+v", ins)
	}
	if ins.ID == "" || ins.Created.IsZero() {
		t.Errorf("insight has no identity or timestamp: %+v", ins)
	}

	// the citations are the item ids that produced the claim — the whole point
	if strings.Join(ins.Items, ",") != "item-a,item-b" {
		t.Errorf("citations = %v, want item-a,item-b newest first", ins.Items)
	}
	if ins.Count != 2 {
		t.Errorf("count = %d, want 2", ins.Count)
	}
	if strings.Join(ins.Sources, ",") != "ANSA,CBC" {
		t.Errorf("sources = %v, want the two reporting sources sorted", ins.Sources)
	}
}

func TestRunRulesIsIdempotentAndRefreshesCitations(t *testing.T) {
	a, rule := ruleFixture(t)

	if _, err := a.runRules("all"); err != nil {
		t.Fatal(err)
	}
	first, ok := a.store.InsightByRule(rule.ID)
	if !ok {
		t.Fatal("the rule did not write an insight")
	}

	// a second run must refresh the one insight, not create a second
	open, err := a.runRules("all")
	if err != nil {
		t.Fatal(err)
	}
	if open != 1 {
		t.Errorf("open = %d after a second run, want 1", open)
	}
	second, ok := a.store.InsightByRule(rule.ID)
	if !ok {
		t.Fatal("the rule did not write an insight")
	}
	if second.ID != first.ID {
		t.Errorf("re-running the rule created a new insight: %q then %q", first.ID, second.ID)
	}
	if !second.Created.Equal(first.Created) {
		t.Errorf("created time was rewritten: %v -> %v", first.Created, second.Created)
	}

	// new material is cited on the next run
	now := time.Now().UTC()
	extra := putItem(t, a.store, "item-d", "Cyber incident follow-up", "CBC", "news",
		now.Add(-10*time.Minute), []string{"cyber"})
	if err := a.store.PutItem(extra); err != nil {
		t.Fatal(err)
	}
	if _, err := a.runRules("all"); err != nil {
		t.Fatal(err)
	}
	third, ok := a.store.InsightByRule(rule.ID)
	if !ok {
		t.Fatal("the rule did not write an insight")
	}
	if third.Count != 3 || !containsFold(third.Items, "item-d") {
		t.Errorf("citations not refreshed: %+v", third)
	}
	if third.ID != first.ID {
		t.Errorf("refresh changed the insight id: %q -> %q", first.ID, third.ID)
	}
}

func TestRunRulesSkipsDisabledRules(t *testing.T) {
	a, rule := ruleFixture(t)
	rule.Enabled = false
	if err := a.store.PutRule(rule); err != nil {
		t.Fatal(err)
	}
	open, err := a.runRules("all")
	if err != nil {
		t.Fatal(err)
	}
	if open != 0 {
		t.Errorf("open = %d, want 0 for a disabled rule", open)
	}
	if insights, _ := a.store.Insights("all"); len(insights) != 0 {
		t.Errorf("a disabled rule wrote %d insights", len(insights))
	}
}

// A rule with no conditions is a pure count rule, which is how "five items in
// 24h" is expressed.
func TestRunRulesSupportsAPureCountRule(t *testing.T) {
	a := analyseFixture(t)
	now := time.Now().UTC()
	for i, name := range []string{"one", "two", "three", "four"} {
		putItem(t, a.store, "item-"+name, "Report "+name, "Wire", "news",
			now.Add(-time.Duration(i)*time.Hour), nil)
	}
	rule := &Rule{Name: "Volume spike", Text: "Four or more items in a day.",
		Severity: "medium", Enabled: true, Window: "24h", MinItems: 4}
	if err := a.store.PutRule(rule); err != nil {
		t.Fatal(err)
	}
	if _, err := a.runRules("all"); err != nil {
		t.Fatal(err)
	}
	ins, ok := a.store.InsightByRule(rule.ID)
	if !ok {
		t.Fatal("the rule did not write an insight")
	}
	if ins.Count != 4 {
		t.Errorf("count = %d, want 4", ins.Count)
	}

	// raise the bar above what the corpus holds: the rule stops firing, and the
	// previously open insight keeps its place in the inbox (it is not erased)
	rule.MinItems = 9
	if err := a.store.PutRule(rule); err != nil {
		t.Fatal(err)
	}
	if _, err := a.runRules("all"); err != nil {
		t.Fatal(err)
	}
	if insights, _ := a.store.Insights("new"); len(insights) != 1 {
		t.Errorf("insights = %d, want the existing one kept", len(insights))
	}
}

// ---------------------------------------------------------------------------
// STX06 — the two retirement commands
// ---------------------------------------------------------------------------

func TestDismissRequiresAReasonAndSurvivesARerun(t *testing.T) {
	a, rule := ruleFixture(t)
	if _, err := a.runRules("all"); err != nil {
		t.Fatal(err)
	}
	ins, ok := a.store.InsightByRule(rule.ID)
	if !ok {
		t.Fatal("the rule did not write an insight")
	}

	// no reason, no dismissal
	if _, err := a.dismissInsight(ins.ID, "   "); err == nil {
		t.Fatal("dismissed an insight without a reason")
	}
	if after, _ := a.store.Insight(ins.ID); after.Status != "new" {
		t.Errorf("status = %q after a refused dismissal", after.Status)
	}

	// the reason is kept, and sanitised like every other stored field
	dismissed, err := a.dismissInsight(ins.ID, "already covered by the desk; see https://evil.example/x desk@evil.example")
	if err != nil {
		t.Fatal(err)
	}
	if dismissed.Status != "dismissed" {
		t.Errorf("status = %q", dismissed.Status)
	}
	if containsLink(dismissed.Reason) || strings.Contains(dismissed.Reason, "@") {
		t.Errorf("reason kept a link or address: %q", dismissed.Reason)
	}
	if !strings.Contains(dismissed.Reason, "already covered by the desk") {
		t.Errorf("reason text lost: %q", dismissed.Reason)
	}

	// the inbox is now empty for that collection
	open, err := a.runRules("all")
	if err != nil {
		t.Fatal(err)
	}
	if open != 0 {
		t.Errorf("open = %d, want 0", open)
	}

	// re-running the rule must not resurrect the analyst's verdict
	again, ok := a.store.InsightByRule(rule.ID)
	if !ok {
		t.Fatal("the rule did not write an insight")
	}
	if again.Status != "dismissed" || again.Reason != dismissed.Reason {
		t.Errorf("a re-run overwrote the verdict: %+v", again)
	}
	if again.Count != dismissed.Count {
		t.Errorf("a dismissed insight's citations were rewritten: %d -> %d", dismissed.Count, again.Count)
	}
}

func TestPromoteTurnsAnInsightIntoADurableNote(t *testing.T) {
	a, rule := ruleFixture(t)
	if _, err := a.runRules("all"); err != nil {
		t.Fatal(err)
	}
	ins, ok := a.store.InsightByRule(rule.ID)
	if !ok {
		t.Fatal("the rule did not write an insight")
	}

	promoted, note, err := a.promoteInsight(ins.ID)
	if err != nil {
		t.Fatal(err)
	}
	if promoted.Status != "promoted" || promoted.NoteID != note.ID {
		t.Errorf("insight not marked promoted: %+v", promoted)
	}
	if note.Subject != ins.ID {
		t.Errorf("note subject = %q, want the insight id", note.Subject)
	}
	if note.Title != ins.Title {
		t.Errorf("note title = %q", note.Title)
	}
	if !containsFold(note.Tags, "insight") || !containsFold(note.Tags, ins.Severity) {
		t.Errorf("note tags = %v, want insight + severity", note.Tags)
	}
	// the note carries the citations, so the claim can be walked back later
	for _, want := range []string{"Citations:", "item-a", "item-b", "Sources:", "CBC"} {
		if !strings.Contains(note.Body, want) {
			t.Errorf("note body is missing %q: %q", want, note.Body)
		}
	}
	if stored, err := a.store.Note(note.ID); err != nil || stored.ID != note.ID {
		t.Errorf("note not stored: %v", err)
	}

	// promoting takes it out of the open inbox, and a re-run leaves it promoted
	open, err := a.runRules("all")
	if err != nil {
		t.Fatal(err)
	}
	if open != 0 {
		t.Errorf("open = %d, want 0 after promotion", open)
	}
	if again, _ := a.store.InsightByRule(rule.ID); again.Status != "promoted" {
		t.Errorf("a re-run un-promoted the insight: %q", again.Status)
	}
}

func TestReopenPutsAnInsightBackInTheInbox(t *testing.T) {
	a, rule := ruleFixture(t)
	if _, err := a.runRules("all"); err != nil {
		t.Fatal(err)
	}
	ins, _ := a.store.InsightByRule(rule.ID)

	if _, err := a.dismissInsight(ins.ID, "noise"); err != nil {
		t.Fatal(err)
	}
	reopened, err := a.reopenInsight(ins.ID)
	if err != nil {
		t.Fatal(err)
	}
	if reopened.Status != "new" || reopened.Reason != "" {
		t.Errorf("reopen left %+v", reopened)
	}
	// it is back in the inbox and counted again
	if insights, _ := a.store.Insights("new"); len(insights) != 1 || insights[0].ID != ins.ID {
		t.Errorf("insights = %+v, want the reopened one", insights)
	}
	if open, err := a.runRules("all"); err != nil || open != 1 {
		t.Errorf("open = %d (err %v), want 1", open, err)
	}

	// and a promoted insight can be reopened too
	if _, _, err := a.promoteInsight(ins.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := a.reopenInsight(ins.ID); err != nil {
		t.Fatal(err)
	}
	if again, _ := a.store.Insight(ins.ID); again.Status != "new" {
		t.Errorf("promoted insight could not be reopened: %q", again.Status)
	}
}

// ---------------------------------------------------------------------------
// STX06 — the rule and insight API
// ---------------------------------------------------------------------------

func TestRuleAndInsightAPISurface(t *testing.T) {
	srv, app := newTestPlatform(t)

	// two items carrying a marker tag, from two different sources
	for _, in := range []struct{ title, source string }{
		{"API rule item one", "CBC"},
		{"API rule item two", "ANSA"},
	} {
		body := `{"title":"` + in.title + `","collection":"news","source":"` + in.source +
			`","categories":["api-rule-marker"]}`
		if code, resp := call(t, srv, http.MethodPost, "/stenella/api/items", body); code != http.StatusCreated {
			t.Fatalf("create item: %d %s", code, resp)
		}
	}

	// --- rules -------------------------------------------------------------
	code, body := call(t, srv, http.MethodPost, "/stenella/api/rules",
		`{"name":"API rule","text":"Two sources tagged api-rule-marker.","severity":"high","enabled":true,
		  "tags":["api-rule-marker"],"window":"24h","minItems":2,"minSources":2}`)
	if code != http.StatusCreated {
		t.Fatalf("create rule: %d %s", code, body)
	}
	rule := mustJSON(t, body)
	ruleID, _ := rule["id"].(string)
	if ruleID == "" {
		t.Fatalf("rule has no id: %s", body)
	}
	if rule["severity"] != "high" {
		t.Errorf("severity = %v", rule["severity"])
	}
	if rule["enabled"] != true {
		t.Errorf("rule did not default to enabled: %v", rule["enabled"])
	}

	if code, _ := call(t, srv, http.MethodPost, "/stenella/api/rules", `{"text":"no name"}`); code != http.StatusBadRequest {
		t.Errorf("unnamed rule accepted")
	}
	if code, body := call(t, srv, http.MethodGet, "/stenella/api/rules/"+ruleID, ""); code != http.StatusOK ||
		!strings.Contains(body, "API rule") {
		t.Errorf("get rule: %d %s", code, body)
	}

	// --- run the rule ------------------------------------------------------
	code, body = call(t, srv, http.MethodPost, "/stenella/api/rules/"+ruleID, `{"action":"run"}`)
	if code != http.StatusOK {
		t.Fatalf("run rule: %d %s", code, body)
	}
	run := mustJSON(t, body)
	if run["fired"] != true || run["count"].(float64) != 2 {
		t.Errorf("run result = %s", body)
	}

	code, body = call(t, srv, http.MethodGet, "/stenella/api/insights", "")
	if code != http.StatusOK {
		t.Fatalf("list insights: %d %s", code, body)
	}
	findMine := func(resp string) map[string]any {
		t.Helper()
		var found map[string]any
		for _, raw := range mustJSON(t, resp)["insights"].([]any) {
			in := raw.(map[string]any)
			if in["rule"] == ruleID {
				found = in
			}
		}
		return found
	}
	ins := findMine(body)
	if ins == nil {
		t.Fatalf("the rule's insight is not in the inbox: %s", body)
	}
	insID, _ := ins["id"].(string)
	if ins["status"] != "new" || ins["severity"] != "high" {
		t.Errorf("insight = %s", body)
	}
	if n, _ := ins["count"].(float64); int(n) != 2 {
		t.Errorf("citations = %v, want 2", ins["count"])
	}
	if ins["ruleName"] != "API rule" {
		t.Errorf("insight does not resolve its rule name: %v", ins["ruleName"])
	}

	// --- dismissal needs a reason -----------------------------------------
	if code, body := call(t, srv, http.MethodPost, "/stenella/api/insights/"+insID, `{"action":"dismiss"}`); code != http.StatusBadRequest {
		t.Errorf("dismissal without a reason: %d %s", code, body)
	}
	code, body = call(t, srv, http.MethodPost, "/stenella/api/insights/"+insID,
		`{"action":"dismiss","reason":"already handled"}`)
	if code != http.StatusOK {
		t.Fatalf("dismiss: %d %s", code, body)
	}
	if got := mustJSON(t, body)["status"]; got != "dismissed" {
		t.Errorf("status = %v", got)
	}
	if _, body := call(t, srv, http.MethodGet, "/stenella/api/insights", ""); findMine(body) != nil {
		t.Error("a dismissed insight is still in the open inbox")
	}
	if _, body := call(t, srv, http.MethodGet, "/stenella/api/insights?status=all", ""); findMine(body) == nil {
		t.Error("a dismissed insight is missing from the archive")
	}

	// --- reopen, then promote ---------------------------------------------
	if code, body := call(t, srv, http.MethodPost, "/stenella/api/insights/"+insID, `{"action":"reopen"}`); code != http.StatusOK {
		t.Fatalf("reopen: %d %s", code, body)
	}
	code, body = call(t, srv, http.MethodPost, "/stenella/api/insights/"+insID, `{"action":"promote"}`)
	if code != http.StatusOK {
		t.Fatalf("promote: %d %s", code, body)
	}
	promoted := mustJSON(t, body)
	note, _ := promoted["note"].(map[string]any)
	if note == nil {
		t.Fatalf("promote returned no note: %s", body)
	}
	noteID, _ := note["id"].(string)
	if status, _ := promoted["insight"].(map[string]any)["status"].(string); status != "promoted" {
		t.Errorf("insight status = %q", status)
	}
	if code, body := call(t, srv, http.MethodGet, "/stenella/api/notes/"+noteID, ""); code != http.StatusOK {
		t.Errorf("promoted note not readable: %d %s", code, body)
	}

	// the note and the insight are real records in the store
	if _, err := app.store.Note(noteID); err != nil {
		t.Errorf("note not stored: %v", err)
	}

	// --- refusals ----------------------------------------------------------
	if code, _ := call(t, srv, http.MethodPost, "/stenella/api/insights/"+insID, `{"action":"shrug"}`); code != http.StatusBadRequest {
		t.Errorf("unknown insight action accepted")
	}
	if code, _ := call(t, srv, http.MethodPost, "/stenella/api/rules/rule-nope", `{"action":"run"}`); code != http.StatusNotFound {
		t.Errorf("running an unknown rule: %d", code)
	}
	if code, _ := call(t, srv, http.MethodPost, "/stenella/api/rules/"+ruleID, `{"action":"shrug"}`); code != http.StatusBadRequest {
		t.Errorf("unknown rule action accepted")
	}

	// disable stops it firing; enable restores it
	if code, body := call(t, srv, http.MethodPost, "/stenella/api/rules/"+ruleID, `{"action":"disable"}`); code != http.StatusOK {
		t.Fatalf("disable: %d %s", code, body)
	} else if mustJSON(t, body)["enabled"] != false {
		t.Errorf("disable left the rule enabled: %s", body)
	}
	if code, body := call(t, srv, http.MethodPost, "/stenella/api/rules/"+ruleID, `{"action":"enable"}`); code != http.StatusOK {
		t.Fatalf("enable: %d %s", code, body)
	} else if mustJSON(t, body)["enabled"] != true {
		t.Errorf("enable left the rule disabled: %s", body)
	}

	// --- delete ------------------------------------------------------------
	if code, body := call(t, srv, http.MethodDelete, "/stenella/api/insights/"+insID, ""); code != http.StatusOK {
		t.Errorf("delete insight: %d %s", code, body)
	}
	if code, body := call(t, srv, http.MethodDelete, "/stenella/api/rules/"+ruleID, ""); code != http.StatusOK {
		t.Errorf("delete rule: %d %s", code, body)
	}
	if _, err := app.store.Rule(ruleID); err == nil {
		t.Error("rule still stored after deletion")
	}
}
