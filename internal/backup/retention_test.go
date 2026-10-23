package backup

import (
	"reflect"
	"testing"
	"time"
)

func TestRetentionCalendarTiers(t *testing.T) {
	snapshots := []Snapshot{}
	for _, row := range [][2]string{
		{"latest", "2026-03-03T12:00:00Z"}, {"same-day", "2026-03-03T11:00:00Z"},
		{"daily", "2026-03-02T12:00:00Z"}, {"weekly", "2026-02-27T12:00:00Z"},
		{"old-week", "2026-02-20T12:00:00Z"}, {"monthly", "2026-01-30T12:00:00Z"},
		{"old-month", "2025-12-30T12:00:00Z"},
	} {
		date, _ := time.Parse(time.RFC3339, row[1])
		snapshots = append(snapshots, Snapshot{ID: row[0], CreatedAt: date})
	}
	now, _ := time.Parse(time.RFC3339, "2026-03-04T00:00:00Z")
	for _, tc := range []struct {
		name   string
		policy Retention
		want   []string
	}{
		{"legacy", Retention{KeepLast: 1}, []string{"same-day", "daily", "weekly", "old-week", "monthly", "old-month"}},
		{"union", Retention{KeepLast: 1, KeepDays: 1, KeepDaily: 2, KeepWeekly: 2, KeepMonthly: 3}, []string{"same-day", "old-week", "old-month"}},
		{"invalid-policy", Retention{KeepLast: 1, KeepMonthly: -1}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var got []string
			for _, sn := range RetentionCandidates(snapshots, tc.policy, "", now) {
				got = append(got, sn.ID)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("got %v want %v", got, tc.want)
			}
		})
	}
	snapshots[4].Pinned = true
	got := RetentionCandidates(snapshots, Retention{KeepLast: 1}, "monthly", now)
	for _, sn := range got {
		if sn.ID == "old-week" || sn.ID == "monthly" || sn.ID == "latest" {
			t.Fatal("protected snapshot selected", sn.ID)
		}
	}
}

func TestRetentionISOWeeksAndTimezoneStable(t *testing.T) {
	date := func(s string) time.Time {
		v, e := time.Parse(time.RFC3339, s)
		if e != nil {
			t.Fatal(e)
		}
		return v
	}
	// Dec 31 and Jan 1 belong to the same ISO week, Jan 4 belongs to the next.
	list := []Snapshot{
		{ID: "jan", CreatedAt: date("2021-01-01T12:00:00+08:00")},
		{ID: "dec", CreatedAt: date("2020-12-31T04:00:00Z")},
		{ID: "next", CreatedAt: date("2021-01-04T04:00:00Z")},
	}
	got := RetentionCandidates(list, Retention{KeepLast: 1, KeepWeekly: 2}, "", date("2021-02-01T00:00:00Z"))
	if len(got) != 1 || got[0].ID != "dec" {
		t.Fatalf("ISO week boundary: %+v", got)
	}
	if list[0].ID != "jan" {
		t.Fatal("mutated input ordering")
	}
	// Equal instants resolve deterministically independent of DB order/timezone.
	list = []Snapshot{{ID: "b", CreatedAt: date("2021-01-01T12:00:00+08:00")}, {ID: "a", CreatedAt: date("2021-01-01T04:00:00Z")}}
	got = RetentionCandidates(list, Retention{KeepLast: 1, KeepDaily: 1}, "", date("2021-02-01T00:00:00Z"))
	if len(got) != 1 || got[0].ID != "b" {
		t.Fatal("unstable equal-time ordering")
	}
}
