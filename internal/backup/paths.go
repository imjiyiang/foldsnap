package backup

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
)

var safeID = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,80}$`)

func ValidID(s string) bool { return safeID.MatchString(s) }
func Canonical(path string) (string, error) {
	if strings.HasPrefix(path, "~/") {
		h, _ := os.UserHomeDir()
		path = filepath.Join(h, path[2:])
	}
	if !filepath.IsAbs(path) {
		return "", errors.New("请选择绝对路径")
	}
	resolved, e := filepath.EvalSymlinks(filepath.Clean(path))
	if e != nil {
		return "", e
	}
	return canonicalPlatform(resolved)
}
func Contains(parent, child string) bool {
	r, e := filepath.Rel(parent, child)
	return e == nil && (r == "." || r != ".." && !strings.HasPrefix(r, ".."+string(os.PathSeparator)))
}
func SafeRelative(s string) bool {
	return s != "" && !filepath.IsAbs(s) && s != "." && s != ".." && !strings.HasPrefix(filepath.Clean(s), "../") && !strings.ContainsRune(s, 0)
}
func ResolveWithin(root, relative string) (string, error) {
	if !SafeRelative(relative) {
		return "", errors.New("无效的相对路径")
	}
	p, e := Canonical(filepath.Join(root, relative))
	if e != nil {
		return "", e
	}
	if !Contains(root, p) {
		return "", errors.New("路径越过了允许的目录")
	}
	return p, nil
}
func Validate(p *Plan) error { return ValidateForUpdate(p, nil) }
func ValidateForUpdate(p *Plan, previous *Plan) error {
	if err := ValidateFileFilters(p.FileFilters); err != nil {
		return err
	}
	if strings.TrimSpace(p.Name) == "" {
		return errors.New("请输入计划名称")
	}
	if p.Mode != "snapshot" && p.Mode != "incremental" && p.Mode != "mirror" {
		return errors.New("备份模式无效")
	}
	if !ValidID(p.ID) {
		return errors.New("无效的计划 ID")
	}
	if len(p.Sources) == 0 || len(p.Destinations) == 0 {
		return errors.New("至少需要一个源目录和一个备份目标")
	}
	if p.Retention.KeepLast < 1 {
		p.Retention.KeepLast = 10
	}
	if err := ValidateSchedule(p.Schedule); err != nil {
		return err
	}
	if err := ValidateRetention(p.Retention); err != nil {
		return err
	}
	ids := map[string]bool{}
	sources := []string{}
	targets := []string{}
	enabledSources, enabledTargets := 0, 0
	for i := range p.Sources {
		s := &p.Sources[i]
		if s.ID == "" {
			s.ID = ID("src-")
		}
		if !ValidID(s.ID) || ids[strings.ToLower(s.ID)] {
			return errors.New("源目录 ID 重复或无效")
		}
		ids[strings.ToLower(s.ID)] = true
		if s.Name == "" {
			s.Name = filepath.Base(s.Path)
		}
		if s.TargetPath == "" {
			s.TargetPath = s.Name
		}
		if !SafeRelative(s.TargetPath) {
			return errors.New("目录映射必须是安全的相对路径")
		}

		var prior *Source
		if previous != nil {
			for _, old := range previous.Sources {
				if old.ID == s.ID {
					copy := old
					prior = &copy
					break
				}
			}
		}
		bound, e := validateSource(*s, prior)
		if e != nil {
			return fmt.Errorf("源目录 %s: %w", s.Name, e)
		}
		*s = bound
		path := s.Path

		for _, a := range sources {
			if PathsOverlap(a, path) {
				return errors.New("源目录存在重复或父子重叠")
			}
		}
		sources = append(sources, path)
		for _, a := range targets {
			if Contains(a, s.TargetPath) || Contains(s.TargetPath, a) {
				return errors.New("源目录的目标映射不能重叠")
			}
		}
		targets = append(targets, s.TargetPath)
		if s.Enabled {
			enabledSources++
		}
		if _, e = CompileSelection(*p, *s); e != nil {
			return e
		}
	}
	ids = map[string]bool{}
	destPaths := []string{}
	for i := range p.Destinations {
		d := &p.Destinations[i]
		if d.ID == "" {
			d.ID = ID("dst-")
		}
		if !ValidID(d.ID) || ids[strings.ToLower(d.ID)] {
			return errors.New("目标 ID 重复或无效")
		}
		ids[strings.ToLower(d.ID)] = true
		if d.VolumeID != "" {
			if root, err := CheckDestination(*d); err == nil {
				d.Path = filepath.Dir(root)
			}
		}
		path, e := Canonical(d.Path)
		if e != nil {
			return fmt.Errorf("备份目标: %w", e)
		}
		st, e := os.Stat(path)
		if e != nil || !st.IsDir() {
			return errors.New("备份目标必须是已存在的目录")
		}
		for _, s := range sources {
			if PathsOverlap(s, path) {
				return errors.New("源目录和备份目标不能相互包含")
			}
		}
		for _, a := range destPaths {
			if PathsOverlap(a, path) {
				return errors.New("备份目标重复或重叠")
			}
		}
		destPaths = append(destPaths, path)
		v, e := VolumeID(path)
		if e != nil {
			return e
		}
		if d.VolumeID != "" && d.VolumeID != v {
			return errors.New("目标磁盘身份发生变化，请重新选择目标")
		}
		d.VolumeID = v
		d.MountPath, e = mountPoint(path)
		if e != nil {
			return e
		}
		d.VolumeRelativePath, e = RelativeToVolume(d.MountPath, path)
		if e != nil {
			return e
		}
		d.Path = path
		if d.Name == "" {
			d.Name = filepath.Base(path)
		}
		if d.Enabled {
			enabledTargets++
		}
	}
	if enabledSources == 0 || enabledTargets == 0 {
		return errors.New("至少启用一个源目录和一个目标")
	}
	return nil
}

// RelativeToVolume accounts for APFS Data firmlinks (/Users is physically on
// /System/Volumes/Data) without recording a relative path that escapes the volume.
func RelativeToVolume(mount, path string) (string, error) {
	relative, err := filepath.Rel(mount, path)
	if err == nil && (relative == "." || SafeRelative(relative)) {
		return relative, nil
	}
	relative = strings.TrimPrefix(filepath.Clean(path), string(os.PathSeparator))
	original, e1 := os.Stat(path)
	physical, e2 := os.Stat(filepath.Join(mount, relative))
	if SafeRelative(relative) && e1 == nil && e2 == nil && os.SameFile(original, physical) {
		return relative, nil
	}
	return "", errors.New("无法确定目标目录在卷内的相对位置")
}

type Repository struct {
	Version  int    `json:"version"`
	ID       string `json:"id"`
	VolumeID string `json:"volumeId"`
}

// CheckDestination resolves a re-mounted external volume by UUID, never by its label alone.
func CheckDestination(d Destination) (string, error) {
	// The saved path is normally still correct. Do not open /Volumes and wake or
	// query unrelated disks before checking this exact bound destination.
	check := func(candidate string) (string, bool) {
		p, e := Canonical(candidate)
		if e != nil || p != candidate {
			return "", false
		}
		v, e := VolumeID(p)
		return filepath.Join(p, "LocalBackup"), e == nil && v == d.VolumeID
	}
	if root, ok := check(d.Path); ok {
		return root, nil
	}
	if d.MountPath != "" && strings.HasPrefix(d.MountPath, "/Volumes/") && (d.VolumeRelativePath == "." || SafeRelative(d.VolumeRelativePath)) {
		rows, _ := os.ReadDir("/Volumes")
		for _, row := range rows {
			if row.IsDir() {
				candidate := filepath.Join("/Volumes", row.Name(), d.VolumeRelativePath)
				if candidate == d.Path {
					continue
				}
				if root, ok := check(candidate); ok {
					return root, nil
				}
			}
		}
	}
	return "", errors.New("目标磁盘未连接或卷身份不匹配，已停止写入")
}

func OpenRepository(d Destination, create bool) (Repository, string, error) {
	var r Repository
	root, e := CheckDestination(d)
	if e != nil {
		return r, "", e
	}
	if st, e := os.Lstat(root); e == nil && st.Mode()&os.ModeSymlink != 0 {
		return r, "", errors.New("仓库目录不能是符号链接")
	}
	file := filepath.Join(root, "repository.json")
	if st, err := os.Lstat(file); err == nil && st.Mode()&os.ModeSymlink != 0 {
		return r, "", errors.New("仓库标识不能是符号链接")
	}
	b, e := ReadMetadataFile(file, 64*1024)
	if os.IsNotExist(e) && create {
		if e = os.MkdirAll(root, 0700); e != nil {
			return r, "", e
		}
		r = Repository{1, ID("repo-"), d.VolumeID}
		b, _ = json.MarshalIndent(r, "", "  ")
		e = writeAtomic(file, b)
	} else if e == nil {
		e = json.Unmarshal(b, &r)
	}
	if e != nil {
		return r, "", e
	}
	if r.Version != 1 || !ValidID(r.ID) || r.VolumeID != d.VolumeID {
		return r, "", errors.New("仓库格式或卷身份无效")
	}
	return r, root, nil
}
func writeAtomic(path string, b []byte) error {
	f, e := os.CreateTemp(filepath.Dir(path), ".commit-")
	if e != nil {
		return e
	}
	name := f.Name()
	defer os.Remove(name)
	if _, e = f.Write(b); e == nil {
		e = f.Sync()
	}
	ce := f.Close()
	if e == nil {
		e = ce
	}
	if e != nil {
		return e
	}
	return os.Rename(name, path)
}

// LockRepository coordinates separate service processes sharing a destination.
// Every writer holds an exclusive advisory lock; restores hold a shared lock.
func LockRepository(d Destination, exclusive, create bool) (func(), error) {
	root, e := CheckDestination(d)
	if e != nil {
		return nil, e
	}
	if st, e := os.Lstat(root); e == nil && st.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("仓库目录不能是符号链接")
	}
	if create {
		if e = os.MkdirAll(root, 0700); e != nil {
			return nil, e
		}
	}
	flags := os.O_RDONLY | syscall.O_NOFOLLOW | syscall.O_NONBLOCK
	if exclusive {
		flags = os.O_CREATE | os.O_RDWR | syscall.O_NOFOLLOW | syscall.O_NONBLOCK
	}
	f, e := os.OpenFile(filepath.Join(root, ".write.lock"), flags, 0600)
	if !exclusive && os.IsNotExist(e) {
		return func() {}, nil
	}
	if e != nil {
		return nil, e
	}
	stat, statErr := f.Stat()
	if statErr != nil || !stat.Mode().IsRegular() {
		f.Close()
		return nil, errors.New("仓库锁必须是普通文件")
	}
	mode := syscall.LOCK_SH | syscall.LOCK_NB
	if exclusive {
		mode = syscall.LOCK_EX | syscall.LOCK_NB
	}
	if e = syscall.Flock(int(f.Fd()), mode); e != nil {
		f.Close()
		return nil, errors.New("该备份仓库正被另一个文件任务使用")
	}
	return func() { syscall.Flock(int(f.Fd()), syscall.LOCK_UN); f.Close() }, nil
}
