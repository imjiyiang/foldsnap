package backup

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/text/cases"
	"golang.org/x/text/unicode/norm"
)

type FilesystemNames struct {
	CaseSensitive      bool `json:"caseSensitive"`
	NormalizesUnicode  bool `json:"normalizesUnicode"`
	TrimsTrailingDot   bool `json:"trimsTrailingDot"`
	TrimsTrailingSpace bool `json:"trimsTrailingSpace"`
}

// Probe only the new private staging tree; no source or historical file is touched.
func ProbeFilesystemNames(stage string) (FilesystemNames, error) {
	caps := FilesystemNames{CaseSensitive: true}
	probe, e := os.MkdirTemp(stage, ".names-")
	if e != nil {
		return caps, e
	}
	defer os.RemoveAll(probe)
	aliases := func(base, variant string) (bool, error) {
		path := filepath.Join(probe, base)
		if e := os.WriteFile(path, []byte("probe"), 0600); e != nil {
			return false, e
		}
		a, e := os.Stat(path)
		if e != nil {
			return false, e
		}
		b, e := os.Stat(filepath.Join(probe, variant))
		if os.IsNotExist(e) {
			return false, nil
		}
		if e != nil {
			return false, e
		}
		return os.SameFile(a, b), nil
	}
	same, e := aliases("case-probe", "CASE-PROBE")
	if e != nil {
		return caps, e
	}
	caps.CaseSensitive = !same
	caps.NormalizesUnicode, e = aliases("unicode-\u00e9", "unicode-e\u0301")
	if e != nil {
		return caps, e
	}
	caps.TrimsTrailingDot, e = aliases("dot-probe", "dot-probe.")
	if e != nil {
		return caps, e
	}
	caps.TrimsTrailingSpace, e = aliases("space-probe", "space-probe ")
	return caps, e
}
func ValidateViewNames(v View, caps FilesystemNames) error {
	seen := map[string]string{}
	fold := cases.Fold()
	key := func(s string) string {
		parts := strings.Split(s, "/")
		for i, p := range parts {
			trim := ""
			if caps.TrimsTrailingDot {
				trim += "."
			}
			if caps.TrimsTrailingSpace {
				trim += " "
			}
			if trim != "" {
				p = strings.TrimRight(p, trim)
			}
			if caps.NormalizesUnicode {
				p = norm.NFD.String(p)
			}
			if !caps.CaseSensitive {
				p = fold.String(p)
			}
			parts[i] = p
		}
		return strings.Join(parts, "/")
	}
	check := func(path string) error {
		k := key(path)
		if old, ok := seen[k]; ok && old != path {
			return fmt.Errorf("目标文件系统无法区分两个路径，备份未发布: %s ↔ %s", old, path)
		}
		seen[k] = path
		return nil
	}
	for _, src := range v.Sources {
		if e := check(src.ID); e != nil {
			return e
		}
	}
	for _, en := range v.Entries {
		if e := check(en.SourceID + "/" + en.Path); e != nil {
			return e
		}
	}
	return nil
}

// PhysicalContains handles case aliases and other existing directory aliases
// without writing probes into source directories.
func PhysicalContains(parent, child string) bool {
	if Contains(parent, child) {
		return true
	}
	ancestor, e := os.Stat(parent)
	if e != nil || !ancestor.IsDir() {
		return false
	}
	for path := filepath.Clean(child); ; path = filepath.Dir(path) {
		st, e := os.Stat(path)
		if e == nil && os.SameFile(ancestor, st) {
			return true
		}
		next := filepath.Dir(path)
		if next == path {
			break
		}
	}
	return false
}
func PathsOverlap(a, b string) bool { return PhysicalContains(a, b) || PhysicalContains(b, a) }
