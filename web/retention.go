package web

import (
	"context"
	"log"
	"sort"
	"sync"
	"time"

	"azzurrotech/stenella/links"
)

// Retention defaults. The window is clamped to a day so a zero or absurd value
// cannot make a client either immortal or wiped on the next sweep.
const (
	// DefaultRetentionDays is the plan's 24h default, expressed in days.
	DefaultRetentionDays = 1
	MinRetentionDays     = 1
	MaxRetentionDays     = 365
	// TombstoneTTL bounds how long a sweep's tombstones are kept for browsers
	// to poll.
	TombstoneTTL = 48 * time.Hour
)

// ClampRetention coerces a requested window into the allowed range.
func ClampRetention(days int) int {
	if days <= 0 {
		return DefaultRetentionDays
	}
	if days < MinRetentionDays {
		return MinRetentionDays
	}
	if days > MaxRetentionDays {
		return MaxRetentionDays
	}
	return days
}

// Tombstone tells a browser that an item it may have cached is gone, so the
// decrypted plaintext held client-side can be dropped. This is the
// "deletion propagates client-side" requirement from plan §4.3: the server never
// pushes, it records, and the page polls.
type Tombstone struct {
	ItemID   string `json:"item_id"`
	Client   string `json:"client"`
	Removed  string `json:"removed"`
	Reason   string `json:"reason"`
	SourceID string `json:"source_id,omitempty"`
}

// SweepReport is the outcome of one retention sweep for one client.
type SweepReport struct {
	Client     string      `json:"client"`
	Examined   int         `json:"examined"`
	Removed    int         `json:"removed"`
	KeptPinned int         `json:"kept_pinned"`
	KeptCmt    int         `json:"kept_commented"`
	KeptLink   int         `json:"kept_linked"`
	Errors     int         `json:"errors"`
	Tombstones []Tombstone `json:"tombstones,omitempty"`
	RanAt      string      `json:"ran_at"`
}

// sweeper owns the hourly retention workflow and the tombstone log. It is a
// named unit so the schedule, the per-client guard and the tombstone window all
// live in one place instead of being spread across Background.
type sweeper struct {
	mu         sync.Mutex
	interval   time.Duration
	lastRun    time.Time
	running    bool
	tombstones map[string][]Tombstone // by client, newest last
}

func newSweeper() *sweeper {
	return &sweeper{
		interval:   time.Hour,
		tombstones: map[string][]Tombstone{},
	}
}

// setInterval is for tests.
func (sw *sweeper) setInterval(d time.Duration) {
	sw.mu.Lock()
	defer sw.mu.Unlock()
	if d > 0 {
		sw.interval = d
	}
}

func (sw *sweeper) due(now time.Time) bool {
	sw.mu.Lock()
	defer sw.mu.Unlock()
	return now.Sub(sw.lastRun) >= sw.interval
}

// begin claims the sweep slot, returning false when one is already in flight.
//
// The guard is platform-wide rather than per client on purpose. Retention writes
// to the same pod tables and cache files a fetch is writing to, and a sweep that
// raced itself would prune ids a parallel sweep had just re-evaluated. Skipping
// is the safe answer: the next tick is at most an hour away, and a manual
// "sweep now" that lands during the hourly run can simply be repeated.
func (sw *sweeper) begin(now time.Time) bool {
	sw.mu.Lock()
	defer sw.mu.Unlock()
	if sw.running {
		return false
	}
	sw.running = true
	sw.lastRun = now
	return true
}

func (sw *sweeper) end() {
	sw.mu.Lock()
	defer sw.mu.Unlock()
	sw.running = false
}

// addTombstones records removals and prunes entries older than the TTL.
func (sw *sweeper) addTombstones(client string, ts []Tombstone) {
	if len(ts) == 0 {
		return
	}
	now := time.Now().UTC()
	sw.mu.Lock()
	defer sw.mu.Unlock()
	all := append(sw.tombstones[client], ts...)
	cutoff := now.Add(-TombstoneTTL)
	kept := all[:0]
	for _, t := range all {
		if at, err := time.Parse(time.RFC3339, t.Removed); err == nil && at.Before(cutoff) {
			continue
		}
		kept = append(kept, t)
	}
	sw.tombstones[client] = kept
}

// Since returns tombstones recorded after the given time, for the browser to
// reconcile against its local cache.
func (sw *sweeper) Since(client string, since time.Time) []Tombstone {
	sw.mu.Lock()
	defer sw.mu.Unlock()
	out := make([]Tombstone, 0, 8)
	for _, t := range sw.tombstones[client] {
		at, err := time.Parse(time.RFC3339, t.Removed)
		if err != nil {
			continue
		}
		if since.IsZero() || at.After(since) {
			out = append(out, t)
		}
	}
	return out
}

// runRetention sweeps every client. It is the hourly workflow registered by
// Background; runOnce is exposed so an operator (or a test) can trigger it.
func (s *Server) runRetention() []SweepReport {
	var reports []SweepReport
	for _, client := range s.retentionClients() {
		rep := s.SweepClient(client)
		if rep != nil {
			reports = append(reports, *rep)
		}
	}
	return reports
}

// retentionClients lists the clients a sweep should visit: everyone in the atp
// registry plus anyone with a feed file on disk.
func (s *Server) retentionClients() []string {
	seen := map[string]bool{}
	for _, c := range s.feeds.Clients() {
		seen[c] = true
	}
	if clients, err := s.atp.ListClients(); err == nil {
		for _, c := range clients {
			if !c.Disabled {
				seen[c.ID] = true
			}
		}
	}
	out := make([]string, 0, len(seen))
	for c := range seen {
		out = append(out, c)
	}
	sort.Strings(out)
	return out
}

// survivalSet computes the item ids that must survive a sweep: everything
// pinned, everything commented on, and everything reachable from those by
// following the link graph forward. Items not in this set that are past their
// window are swept.
//
// The edges come from the links store — the one graph in the system, which also
// powers shares and the links pane. Edges, not Links, because Links enriches
// every row with a resolved title and this walk needs none of them.
//
// The traversal visits every reachable node, not just the ones currently inside
// the retention window: a young node is alive anyway, and it can still point at
// something older that must be kept. It is a breadth-first traversal over one
// snapshot of the edges, so the cost is O(edges) per sweep rather than a graph
// query per candidate — the mitigation for plan risk R3.
func (s *Server) survivalSet(client string) (map[string]bool, error) {
	edges, err := s.links.Edges(client)
	if err != nil {
		return nil, err
	}
	pinned, err := s.collab.PinnedItems(client)
	if err != nil {
		return nil, err
	}
	commented, err := s.collab.CommentRefs(client)
	if err != nil {
		return nil, err
	}
	keep := map[string]bool{}
	adjacency := map[string][]string{}
	for _, e := range edges {
		// Only item→item edges carry a retention exemption. A link to a URL
		// points at something the sweep does not own.
		if e.ToKind != links.ToItem {
			continue
		}
		adjacency[e.FromID] = append(adjacency[e.FromID], e.ToID)
	}
	for from := range adjacency {
		sort.Strings(adjacency[from])
	}
	// Seeds are sorted so the queue order — and therefore any reported order —
	// does not depend on map iteration.
	seeds := make([]string, 0, len(pinned)+len(commented))
	for id := range pinned {
		seeds = append(seeds, id)
	}
	for id := range commented {
		seeds = append(seeds, id)
	}
	sort.Strings(seeds)
	for _, id := range seeds {
		keep[id] = true
	}
	queue := seeds
	for len(queue) > 0 {
		id := queue[0]
		queue = queue[1:]
		for _, next := range adjacency[id] {
			if keep[next] {
				continue
			}
			keep[next] = true
			queue = append(queue, next)
		}
	}
	return keep, nil
}

// SweepClient drops one client's expired items from the feed cache and from pod,
// honouring the pin, comment and link exceptions. It returns nil when there is
// nothing to do, so a caller iterating clients can skip it.
func (s *Server) SweepClient(client string) *SweepReport {
	if !s.sweep.begin(time.Now().UTC()) {
		// A sweep is already in flight. Returning rather than queueing keeps two
		// sweeps from racing on the same rows; the running one will finish.
		return nil
	}
	defer s.sweep.end()

	keep, err := s.survivalSet(client)
	if err != nil {
		log.Printf("retention: %s: survival set: %v", client, err)
		return nil
	}
	pinned, err := s.collab.PinnedItems(client)
	if err != nil {
		pinned = map[string]bool{}
	}
	commented, err := s.collab.CommentRefs(client)
	if err != nil {
		commented = map[string]bool{}
	}

	now := time.Now().UTC()
	rep := &SweepReport{Client: client, RanAt: now.Format(time.RFC3339)}
	table := links.ItemsTable(client)

	// One pass over the cached items decides what dies. The per-source cutoff
	// honours each source's own retention window.
	for _, src := range s.feeds.Sources(client) {
		cutoff := now.Add(-time.Duration(ClampRetention(src.Retention)) * 24 * time.Hour)
		expired := s.feeds.Expired(client, src.ID, cutoff)
		if len(expired) == 0 {
			continue
		}
		rep.Examined += len(expired)
		// Collected per source so the cache prune below is exactly this source's
		// removals. Pruning with the whole report's ids would also be harmless
		// (unknown ids are ignored) but it makes the call grow with the report.
		var gone []string
		for _, id := range expired {
			switch {
			case pinned[id]:
				rep.KeptPinned++
				continue
			case commented[id]:
				rep.KeptCmt++
				continue
			case keep[id]:
				// Reachable from a pinned or commented item through the graph.
				rep.KeptLink++
				continue
			}
			if err := s.atp.DeleteRecord(table, id); err != nil && !isNotFound(err) {
				rep.Errors++
				continue
			}
			rep.Removed++
			gone = append(gone, id)
			rep.Tombstones = append(rep.Tombstones, Tombstone{
				ItemID: id, Client: client, SourceID: src.ID,
				Removed: now.Format(time.RFC3339), Reason: "retention",
			})
		}
		// The cache is the read path; a pod row left behind with no cache entry
		// would surface in the database view forever.
		s.feeds.Prune(client, src.ID, gone)
	}

	if len(rep.Tombstones) > 0 {
		s.sweep.addTombstones(client, rep.Tombstones)
	}
	if rep.Examined == 0 {
		return nil
	}
	return rep
}

// retentionLoop runs the sweep on its interval until the context is done. It is
// separate from the feed refresher so a long fetch cannot delay a sweep, and a
// failing sweep cannot stop refreshes.
func (s *Server) retentionLoop(ctx context.Context) {
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if !s.sweep.due(time.Now().UTC()) {
				continue
			}
			for _, rep := range s.runRetention() {
				if rep.Removed > 0 || rep.Errors > 0 {
					log.Printf("retention: %s examined=%d removed=%d kept(pin=%d comment=%d link=%d) errors=%d",
						rep.Client, rep.Examined, rep.Removed,
						rep.KeptPinned, rep.KeptCmt, rep.KeptLink, rep.Errors)
				}
			}
		}
	}
}

// retentionWindow returns a client's effective default window in days, for the
// UI. Per-source windows override this.
func (s *Server) retentionWindow(client string) int {
	day := DefaultRetentionDays
	for _, src := range s.feeds.Sources(client) {
		if src.Retention > 0 {
			return ClampRetention(src.Retention)
		}
		day = DefaultRetentionDays
	}
	return day
}
