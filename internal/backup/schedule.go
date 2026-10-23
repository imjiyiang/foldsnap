package backup

import "errors"

const MaxScheduleMinutes = 365 * 24 * 60
const MaxChangeBytes int64 = 1 << 50

func ValidateSchedule(s Schedule) error {
	if s.IntervalMinutes < 0 || s.IntervalMinutes > MaxScheduleMinutes || s.MaxIntervalMinutes < 0 || s.MaxIntervalMinutes > MaxScheduleMinutes {
		return errors.New("调度间隔须在 0 至 525600 分钟之间")
	}
	if s.DebounceSeconds < 0 || s.DebounceSeconds > 86400 || (s.DebounceSeconds > 0 && s.DebounceSeconds < 10) {
		return errors.New("静默等待须为 0（默认 120 秒）或 10 至 86400 秒")
	}
	if s.MinChangedFiles < 0 || s.MinChangedFiles > 1000000 || s.MinChangedBytes < 0 || s.MinChangedBytes > MaxChangeBytes {
		return errors.New("变化阈值超出支持范围（100 万个路径、1 PiB）")
	}
	if s.MinIntervalMinutes != nil && (*s.MinIntervalMinutes < 0 || *s.MinIntervalMinutes > MaxScheduleMinutes) {
		return errors.New("变化触发的最短间隔须在 0 至 525600 分钟之间")
	}
	if s.MaxIntervalMinutes > 0 && s.MaxIntervalMinutes < s.ChangeIntervalMinutes() {
		return errors.New("最长检查间隔不能小于变化触发的最短间隔")
	}
	if s.MaxAttempts != nil && (*s.MaxAttempts < 1 || *s.MaxAttempts > 10) {
		return errors.New("自动任务尝试次数须在 1 至 10 次之间")
	}
	return nil
}
func (s Schedule) ChangeIntervalMinutes() int {
	if s.MinIntervalMinutes == nil {
		return 1
	}
	return *s.MinIntervalMinutes
}
func (s Schedule) AutomaticAttempts() int {
	if s.MaxAttempts == nil {
		return 3
	}
	return *s.MaxAttempts
}
func (s Schedule) QuietSeconds() int {
	if s.DebounceSeconds < 10 {
		return 120
	}
	return s.DebounceSeconds
}
