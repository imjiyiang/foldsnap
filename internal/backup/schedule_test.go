package backup

import "testing"

func TestScheduleDefaultsAndBounds(t *testing.T) {
	s := Schedule{}
	if s.ChangeIntervalMinutes() != 1 || s.AutomaticAttempts() != 3 || s.QuietSeconds() != 120 {
		t.Fatal("legacy defaults changed")
	}
	zero := 0
	s.MinIntervalMinutes = &zero
	if s.ChangeIntervalMinutes() != 0 {
		t.Fatal("explicit zero did not disable change interval")
	}
	invalid := []Schedule{{IntervalMinutes: MaxScheduleMinutes + 1}, {DebounceSeconds: 9}, {MinChangedBytes: MaxChangeBytes + 1}, {MinChangedFiles: 1000001}, {MaxIntervalMinutes: MaxScheduleMinutes + 1}}
	for _, s := range invalid {
		if err := ValidateSchedule(s); err == nil {
			t.Fatal("unbounded schedule accepted", s)
		}
	}
}
