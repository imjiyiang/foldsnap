package backup

import (
	"strings"
	"time"
)

const walkSelectionLimit = 4096

type pathDecision struct {
	include bool
	reason  string
}

// This matcher belongs to one serial source walk with an immutable policy.
// Only metadata-independent parent rule decisions are reused. File filters,
// exceptions and all filesystem/version/content checks still run per entry.
type walkSelection struct {
	selection Selection
	parents   map[string]pathDecision
}

func newWalkSelection(s Selection) *walkSelection {
	return &walkSelection{selection: s, parents: make(map[string]pathDecision)}
}
func (w *walkSelection) remember(rel string, d pathDecision) {
	if len(w.parents) >= walkSelectionLimit {
		clear(w.parents)
	}
	w.parents[rel] = d
}
func (w *walkSelection) parent(rel string) pathDecision {
	if d, ok := w.parents[rel]; ok {
		return d
	}
	d := w.pathSelected(rel)
	w.remember(rel, d)
	return d
}
func (w *walkSelection) pathSelected(rel string) pathDecision {
	inherited := pathDecision{true, "默认包含"}
	if slash := strings.LastIndexByte(rel, '/'); slash >= 0 {
		inherited = w.parent(rel[:slash])
		if !inherited.include {
			return inherited
		}
	}
	include, reason := MatchRules(rel, w.selection.hidden, w.selection.rules)
	if include && reason == "默认包含" {
		return inherited
	}
	return pathDecision{include, reason}
}
func (w *walkSelection) Match(rel, kind string, size int64, mtime time.Time) (bool, string) {
	d := w.pathSelected(rel)
	if kind == "directory" {
		w.remember(rel, d)
	}
	return w.selection.matchPath(rel, kind, size, mtime, d.include, d.reason)
}
