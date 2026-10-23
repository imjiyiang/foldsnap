package backup

import (
	"fmt"
	"path/filepath"
	"strings"
)

// Validate configuration without binding a new source or requiring offline
// volumes to be present. Restored plans must remain paused until reviewed.
func ValidateRecoveryPlan(p Plan) error {
	if !ValidID(p.ID) || strings.TrimSpace(p.Name) == "" || (p.Mode != "snapshot" && p.Mode != "incremental" && p.Mode != "mirror") {
		return fmt.Errorf("恢复计划基本信息无效")
	}
	if len(p.Sources) == 0 || len(p.Destinations) == 0 || p.Retention.KeepLast < 1 || p.Retention.KeepDays < 0 {
		return fmt.Errorf("恢复计划缺少目录、目标或保留策略")
	}
	if err := ValidateRetention(p.Retention); err != nil {
		return err
	}
	if err := ValidateSchedule(p.Schedule); err != nil {
		return err
	}
	cleanPath := func(path string) bool {
		return filepath.IsAbs(path) && filepath.Clean(path) == path && !strings.ContainsRune(path, 0)
	}
	enabledSources, enabledTargets := 0, 0
	ids := map[string]bool{}
	for i, s := range p.Sources {
		if s.Enabled {
			enabledSources++
		}
		id := strings.ToLower(s.ID)
		if !ValidID(s.ID) || ids[id] || !cleanPath(s.Path) || !SafeRelative(s.TargetPath) || !SourceBound(s) {
			return fmt.Errorf("恢复源目录身份或路径无效: %s", s.Name)
		}
		ids[id] = true
		if _, err := CompileSelection(p, s); err != nil {
			return err
		}
		for _, other := range p.Sources[:i] {
			if PathsOverlap(other.Path, s.Path) || Contains(other.TargetPath, s.TargetPath) || Contains(s.TargetPath, other.TargetPath) {
				return fmt.Errorf("恢复源目录或命名空间重叠")
			}
		}
	}
	ids = map[string]bool{}
	for i, d := range p.Destinations {
		if d.Enabled {
			enabledTargets++
		}
		id := strings.ToLower(d.ID)
		if !ValidID(d.ID) || ids[id] || !cleanPath(d.Path) || d.VolumeID == "" {
			return fmt.Errorf("恢复目标身份或路径无效")
		}
		ids[id] = true
		for _, s := range p.Sources {
			if PathsOverlap(s.Path, d.Path) {
				return fmt.Errorf("恢复源与目标重叠")
			}
		}
		for _, other := range p.Destinations[:i] {
			if PathsOverlap(other.Path, d.Path) {
				return fmt.Errorf("恢复目标相互重叠")
			}
		}
	}
	if enabledSources == 0 || enabledTargets == 0 {
		return fmt.Errorf("恢复计划缺少启用源或目标")
	}
	return nil
}
