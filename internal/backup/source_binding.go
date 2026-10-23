package backup

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

var ErrSourceUnavailable = errors.New("源目录或原磁盘暂不可用")
var ErrSourceChanged = errors.New("源目录身份发生变化")
var ErrSourceUnbound = errors.New("源目录尚未绑定身份，请编辑并保存计划")

func SourceBound(s Source) bool { return s.VolumeID != "" && s.DirectoryID != "" }
func sameSourceIdentity(a, b Source) bool {
	return a.VolumeID == b.VolumeID && a.DirectoryID == b.DirectoryID && a.DirectoryBirthTime == b.DirectoryBirthTime
}

// BindSource captures an exact selected root, never a replacement behind a
// saved identity. Directory ctime is intentionally excluded: contents/renames
// change it. APFS creation time guards against reuse of a deleted inode.
func BindSource(s Source) (Source, error) {
	st, err := os.Lstat(s.Path)
	if err != nil {
		return s, fmt.Errorf("%w: %s", ErrSourceUnavailable, s.Name)
	}
	if !st.IsDir() {
		return s, fmt.Errorf("%w: %s 不是目录或已被链接替代", ErrSourceChanged, s.Name)
	}
	path, err := Canonical(s.Path)
	if err != nil {
		return s, fmt.Errorf("%w: %s", ErrSourceUnavailable, s.Name)
	}
	st, err = os.Lstat(path)
	if err != nil || !st.IsDir() {
		return s, fmt.Errorf("%w: %s", ErrSourceChanged, s.Name)
	}
	volume, err := VolumeID(path)
	if err != nil {
		return s, fmt.Errorf("%w: %s", ErrSourceUnavailable, s.Name)
	}
	en := Entry{}
	populateIdentity(&en, st, volume)
	captured := s
	captured.Path = path
	captured.VolumeID = volume
	captured.DirectoryID = en.FileID
	captured.DirectoryBirthTime = strconv.FormatInt(en.BirthTime, 10)
	if captured.DirectoryID == "" {
		return s, fmt.Errorf("%w: 无法读取目录 ID", ErrSourceUnavailable)
	}
	if SourceBound(s) && !sameSourceIdentity(s, captured) {
		if s.VolumeID != captured.VolumeID {
			return s, fmt.Errorf("%w: %s 正在等待原卷 UUID %s", ErrSourceUnavailable, s.Name, s.VolumeID)
		}
		return s, fmt.Errorf("%w: %s；请重新选择原目录，其他目录须添加为新源", ErrSourceChanged, s.Name)
	}
	captured.MountPath, err = mountPoint(path)
	if err != nil {
		return s, err
	}
	captured.VolumeRelativePath, err = RelativeToVolume(captured.MountPath, path)
	if err != nil {
		return s, err
	}
	latest, err := os.Lstat(path)
	if err != nil || !latest.IsDir() || !os.SameFile(st, latest) {
		return s, fmt.Errorf("%w: %s 在读取身份时发生变化", ErrSourceChanged, s.Name)
	}
	latestEntry := Entry{}
	populateIdentity(&latestEntry, latest, volume)
	if latestEntry.BirthTime != en.BirthTime {
		return s, fmt.Errorf("%w: %s 在读取身份时发生变化", ErrSourceChanged, s.Name)
	}
	return captured, nil
}

// ResolveSource only searches alternate mount roots, never the whole disk for
// similarly named directories. Every candidate must match the saved identity.
func ResolveSource(s Source) (Source, error) {
	if !SourceBound(s) {
		return s, fmt.Errorf("%w: %s", ErrSourceUnbound, s.Name)
	}
	resolved, originalErr := BindSource(s)
	if originalErr == nil {
		return resolved, nil
	}
	if strings.HasPrefix(s.MountPath, "/Volumes/") && (s.VolumeRelativePath == "." || SafeRelative(s.VolumeRelativePath)) {
		rows, _ := os.ReadDir("/Volumes")
		roots := []string{}
		for _, row := range rows {
			if row.IsDir() {
				roots = append(roots, filepath.Join("/Volumes", row.Name()))
			}
		}
		return resolveSourceAtMounts(s, roots, originalErr)
	}
	return s, originalErr
}
func resolveSourceAtMounts(s Source, roots []string, originalErr error) (Source, error) {
	for _, root := range roots {
		candidate := s
		candidate.Path = filepath.Join(root, s.VolumeRelativePath)
		if resolved, err := BindSource(candidate); err == nil {
			return resolved, nil
		}
	}
	return s, originalErr
}

func ResolveSources(p Plan) (Plan, error) {
	p.Sources = append([]Source{}, p.Sources...)
	roots := []string{}
	for _, d := range p.Destinations {
		if root, err := CheckDestination(d); err == nil {
			roots = append(roots, filepath.Dir(root))
		}
	}
	for i, src := range p.Sources {
		if !src.Enabled {
			continue
		}
		resolved, err := ResolveSource(src)
		if err != nil {
			return p, err
		}
		p.Sources[i] = resolved
	}
	for i, src := range p.Sources {
		if !src.Enabled {
			continue
		}
		for j, other := range p.Sources {
			if j < i && other.Enabled && PathsOverlap(src.Path, other.Path) {
				return p, errors.New("源目录重新定位后相互重叠")
			}
		}
		for _, root := range roots {
			if PathsOverlap(src.Path, root) {
				return p, errors.New("源目录重新定位后与备份目标重叠")
			}
		}
	}
	return p, nil
}

// Only server-provided previous plans authorize retaining an offline binding.
// Client-supplied fields cannot erase or change an existing source identity.
func validateSource(s Source, previous *Source) (Source, error) {
	if previous != nil && SourceBound(*previous) {
		if (s.VolumeID != "" || s.DirectoryID != "" || s.DirectoryBirthTime != "") && !sameSourceIdentity(s, *previous) {
			return s, errors.New("不能修改已有源的身份；请移除后添加为新源")
		}
		s.VolumeID = previous.VolumeID
		s.DirectoryID = previous.DirectoryID
		s.DirectoryBirthTime = previous.DirectoryBirthTime
		s.MountPath = previous.MountPath
		s.VolumeRelativePath = previous.VolumeRelativePath
		if s.Path == previous.Path {
			resolved, err := ResolveSource(s)
			if err == nil {
				return resolved, nil
			}
			if errors.Is(err, ErrSourceUnavailable) || !s.Enabled {
				return s, nil
			}
			return s, err
		}
	}
	return BindSource(s)
}
