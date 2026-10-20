package autoupload

import (
	"math"
	"sort"
	"time"

	"github.com/LNA-DEV/HomePageCompanion/capability/feed"
	"github.com/LNA-DEV/HomePageCompanion/capability/social"
)

// retryGaps are the waits before an open transient failure is tried again
// automatically: 1, 3 and 7 days after the 1st, 2nd and 3rd failure. After
// that, and for account and content failures always, only by hand (D2).
var retryGaps = []time.Duration{24 * time.Hour, 3 * 24 * time.Hour, 7 * 24 * time.Hour}

// history is what the choice needs to know about one item on the target.
type history struct {
	done       bool // published, skipped or marked by hand
	openFails  int  // failures since the last success
	lastFail   time.Time
	manualOnly bool // an open account or content failure
}

// candidate is an item the next run may post.
type candidate struct {
	item  feed.Item
	retry bool // an open transient failure whose time has come
}

// retryDue reports whether an item with open transient failures may be
// tried again now.
func retryDue(h history, now time.Time) bool {
	if h.openFails == 0 || h.manualOnly || h.openFails > len(retryGaps) {
		return false
	}
	return now.Sub(h.lastFail) >= retryGaps[h.openFails-1]
}

// chooseRotation picks the next photo of a rotation: open transient
// failures whose time has come first (oldest failure first), then the item
// whose date is closest to today, ignoring the year (D1). Ties go to the
// lowest ID, so the schedule can show the choice before it is made; items
// without a date come last.
func chooseRotation(items []feed.Item, hist map[string]history, now time.Time) []candidate {
	var retries, dated, undated []candidate
	for _, it := range items {
		h := hist[it.ID]
		switch {
		case h.done:
			continue
		case h.openFails > 0:
			if retryDue(h, now) {
				retries = append(retries, candidate{item: it, retry: true})
			}
			continue
		case !it.HasDate || it.Published.Year() <= 1:
			undated = append(undated, candidate{item: it})
		default:
			dated = append(dated, candidate{item: it})
		}
	}
	sort.SliceStable(retries, func(i, j int) bool {
		return hist[retries[i].item.ID].lastFail.Before(hist[retries[j].item.ID].lastFail)
	})
	diff := func(p time.Time) float64 {
		adjusted := time.Date(p.Year(), now.Month(), now.Day(), now.Hour(), now.Minute(), now.Second(), 0, time.UTC)
		return math.Abs(adjusted.Sub(p).Seconds())
	}
	sort.SliceStable(dated, func(i, j int) bool {
		di, dj := diff(dated[i].item.Published), diff(dated[j].item.Published)
		if di != dj {
			return di < dj
		}
		return dated[i].item.ID < dated[j].item.ID
	})
	sort.SliceStable(undated, func(i, j int) bool { return undated[i].item.ID < undated[j].item.ID })
	out := append(retries, dated...)
	return append(out, undated...)
}

// historyFrom builds the history of every item on one target.
func historyFrom(pubs []Publication, attempts []Attempt) map[string]history {
	hist := map[string]history{}
	for _, p := range pubs {
		h := hist[p.ItemID]
		h.done = true
		hist[p.ItemID] = h
	}
	for _, a := range attempts {
		if a.Success || a.ResolvedAt != nil {
			continue
		}
		h := hist[a.ItemID]
		h.openFails++
		if a.CreatedAt.After(h.lastFail) {
			h.lastFail = a.CreatedAt
		}
		if a.Cause == social.CauseAccount || a.Cause == social.CauseContent {
			h.manualOnly = true
		}
		hist[a.ItemID] = h
	}
	return hist
}
