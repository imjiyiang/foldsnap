package backup

import (
	"fmt"
	"sort"
	"time"
)

func ValidateRetention(r Retention) error {
	if r.KeepLast < 1 || r.KeepDays < 0 {
		return fmt.Errorf("保留份数至少为 1，天数不能为负数")
	}
	for _, n := range []int{r.KeepDaily, r.KeepWeekly, r.KeepMonthly} {
		if n < 0 || n > 1000 {
			return fmt.Errorf("分层保留数量应在 0 到 1000 之间")
		}
	}
	return nil
}

// RetentionCandidates evaluates one destination, independently of all others.
// Tiers add protection to the existing count/age policy. Each selects the latest
// snapshot in the last N populated UTC calendar buckets (ISO weeks start Monday).
// UTC makes protection stable across machine timezone changes and disk recovery.
func RetentionCandidates(snapshots []Snapshot, r Retention, currentID string, now time.Time) []Snapshot {
	if ValidateRetention(r) != nil {
		return nil
	} // Invalid policy must never delete.
	list := append([]Snapshot(nil), snapshots...)
	sort.Slice(list, func(i, j int) bool {
		if list[i].CreatedAt.Equal(list[j].CreatedAt) {
			return list[i].ID < list[j].ID
		}
		return list[i].CreatedAt.After(list[j].CreatedAt)
	})
	protected := map[string]bool{currentID: true}
	limits := []int{r.KeepDaily, r.KeepWeekly, r.KeepMonthly}
	buckets := []map[string]bool{{}, {}, {}}
	for i, sn := range list {
		if i == 0 || sn.Pinned || sn.CreatedAt.IsZero() || sn.CreatedAt.After(now) {
			protected[sn.ID] = true
		}
		t := sn.CreatedAt.UTC()
		year, week := t.ISOWeek()
		keys := []string{t.Format("2006-01-02"), fmt.Sprintf("%04d-W%02d", year, week), t.Format("2006-01")}
		for tier, key := range keys {
			if !buckets[tier][key] && len(buckets[tier]) < limits[tier] {
				buckets[tier][key] = true
				protected[sn.ID] = true
			}
		}
	}
	candidates := []Snapshot{}
	for i, sn := range list {
		// Float day comparison avoids duration overflow for large legacy day limits.
		tooOld := r.KeepDays > 0 && now.Sub(sn.CreatedAt).Hours()/24 > float64(r.KeepDays)
		if !protected[sn.ID] && (i >= r.KeepLast || tooOld) {
			candidates = append(candidates, sn)
		}
	}
	return candidates
}
