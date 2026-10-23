package backup

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

var ErrViewChanged = errors.New("筛选范围或同步视图已变化，请重新预览")

type FileException struct {
	Path   string `json:"path"`
	Action string `json:"action"`
}

// FileFilters apply to regular files. A source override replaces the complete
// plan-level file policy; path rules still use their existing ordered hierarchy.
type FileFilters struct {
	MinBytes           *int64          `json:"minBytes,omitempty"`
	MaxBytes           *int64          `json:"maxBytes,omitempty"`
	ModifiedWithinDays *int            `json:"modifiedWithinDays,omitempty"`
	Types              []string        `json:"types,omitempty"`
	Exceptions         []FileException `json:"exceptions,omitempty"`
}

var fileTypes = map[string]string{
	"image":    "jpg jpeg png gif webp heic heif tiff tif bmp svg avif raw cr2 cr3 nef arw dng raf orf psd ai",
	"video":    "mp4 mov mkv avi m4v webm mpeg mpg mts m2ts wmv flv",
	"audio":    "mp3 wav aiff aif flac aac m4a ogg opus alac mid midi",
	"document": "pdf txt md markdown rtf doc docx xls xlsx csv tsv ppt pptx odt ods odp pages numbers key epub mobi",
	"archive":  "zip tar gz bz2 xz zst 7z rar dmg iso tgz pkg",
	"code":     "go rs py js jsx ts tsx java c h cpp hpp cc cs swift m mm sh bash zsh fish rb php html css scss json yaml yml toml xml sql vue svelte ipynb lock",
}

var typeByExtension = func() map[string]string {
	out := map[string]string{}
	for category, list := range fileTypes {
		for _, ext := range strings.Fields(list) {
			out[ext] = category
		}
	}
	return out
}()

func FileType(path string) string {
	ext := strings.ToLower(strings.TrimPrefix(filepath.Ext(path), "."))
	if category, ok := typeByExtension[ext]; ok {
		return category
	}
	return "other"
}
func ValidateFileFilters(f FileFilters) error {
	if (f.MinBytes != nil && *f.MinBytes < 0) || (f.MaxBytes != nil && *f.MaxBytes < 0) {
		return errors.New("文件大小不能为负数")
	}
	if f.MinBytes != nil && f.MaxBytes != nil && *f.MinBytes > *f.MaxBytes {
		return errors.New("最小文件大小不能大于最大值")
	}
	if f.ModifiedWithinDays != nil && (*f.ModifiedWithinDays < 1 || *f.ModifiedWithinDays > 36500) {
		return errors.New("修改时间范围应为 1 到 36500 天")
	}
	seen := map[string]bool{}
	for _, v := range f.Types {
		if _, ok := fileTypes[v]; !ok && v != "other" {
			return fmt.Errorf("未知文件类型: %s", v)
		}
		if seen[v] {
			return errors.New("文件类型不能重复")
		}
		seen[v] = true
	}
	if len(f.Exceptions) > 1000 {
		return errors.New("单文件例外不能超过 1000 条")
	}
	seen = map[string]bool{}
	for _, e := range f.Exceptions {
		if !SafeRelative(e.Path) || filepath.ToSlash(filepath.Clean(e.Path)) != e.Path || strings.ContainsAny(e.Path, "\r\n") {
			return errors.New("单文件例外必须是规范的源目录相对路径")
		}
		if e.Action != "include" && e.Action != "exclude" {
			return errors.New("单文件例外只能包含或排除")
		}
		if seen[e.Path] {
			return errors.New("单文件例外路径重复")
		}
		seen[e.Path] = true
	}
	return nil
}

type Selection struct {
	rules      []compiledRule
	hidden     bool
	filters    FileFilters
	at         time.Time
	exceptions map[string]string
	ancestors  map[string]bool
}

func EvaluationTime(p Plan) time.Time {
	if !p.EvaluatedAt.IsZero() {
		return p.EvaluatedAt
	}
	return time.Now().UTC()
}
func CompileSelection(p Plan, s Source) (Selection, error) {
	f := p.FileFilters
	if s.FileFilters != nil {
		f = *s.FileFilters
	}
	if err := ValidateFileFilters(f); err != nil {
		return Selection{}, err
	}
	rules, err := CompileRules(append(append([]Rule{}, s.Rules...), p.Rules...))
	if err != nil {
		return Selection{}, err
	}
	out := Selection{rules: rules, hidden: s.ExcludeHidden, filters: f, at: EvaluationTime(p), exceptions: map[string]string{}, ancestors: map[string]bool{}}
	for _, e := range f.Exceptions {
		out.exceptions[e.Path] = e.Action
		if e.Action == "include" {
			for parent := filepath.Dir(e.Path); parent != "."; parent = filepath.Dir(parent) {
				out.ancestors[filepath.ToSlash(parent)] = true
			}
		}
	}
	return out, nil
}
func (s Selection) pathSelected(rel string) (bool, string) {
	parts := strings.Split(rel, "/")
	reason := "默认包含"
	for i := range parts {
		ok, matchReason := MatchRules(strings.Join(parts[:i+1], "/"), s.hidden, s.rules)
		if !ok {
			return false, matchReason
		}
		if matchReason != "默认包含" {
			reason = matchReason
		}
	}
	return true, reason
}
func (s Selection) includeBelow(rel string) bool { return s.ancestors[rel] }

// Possible is deliberately metadata-independent: changes can move files across
// size/date/type boundaries, and deleted paths no longer have readable metadata.
func (s Selection) Possible(rel string) bool {
	if action, ok := s.exceptions[rel]; ok {
		return action == "include"
	}
	ok, _ := s.pathSelected(rel)
	return ok || s.includeBelow(rel)
}
func (s Selection) Match(rel string, kind string, size int64, mtime time.Time) (bool, string) {
	pathOK, pathReason := s.pathSelected(rel)
	return s.matchPath(rel, kind, size, mtime, pathOK, pathReason)
}

func (s Selection) matchPath(rel string, kind string, size int64, mtime time.Time, pathOK bool, pathReason string) (bool, string) {
	if kind != "directory" {
		if action, ok := s.exceptions[rel]; ok {
			return action == "include", "单文件例外: " + action + " " + rel
		}
	}
	if !pathOK {
		reason := pathReason
		if kind == "directory" && s.includeBelow(rel) {
			return true, "单文件包含所需的父目录"
		}
		return false, reason
	}
	if kind != "file" {
		return true, "默认包含"
	}
	f := s.filters
	if f.MinBytes != nil && size < *f.MinBytes {
		return false, fmt.Sprintf("文件小于 %d 字节", *f.MinBytes)
	}
	if f.MaxBytes != nil && size > *f.MaxBytes {
		return false, fmt.Sprintf("文件大于 %d 字节", *f.MaxBytes)
	}
	if f.ModifiedWithinDays != nil && mtime.Before(s.at.Add(-time.Duration(*f.ModifiedWithinDays)*24*time.Hour)) {
		return false, fmt.Sprintf("修改时间早于最近 %d 天", *f.ModifiedWithinDays)
	}
	if len(f.Types) > 0 {
		kind := FileType(rel)
		found := false
		for _, t := range f.Types {
			if t == kind {
				found = true
				break
			}
		}
		if !found {
			return false, "文件类型未选中: " + kind
		}
	}
	if pathReason != "默认包含" {
		return true, pathReason
	}
	return true, "符合文件筛选条件"
}
func entryKind(st os.FileInfo) string {
	if st.IsDir() {
		return "directory"
	}
	if st.Mode()&os.ModeSymlink != 0 {
		return "symlink"
	}
	if st.Mode().IsRegular() {
		return "file"
	}
	return "special"
}

// Mirror scope is evaluated against live metadata when available. Falling back
// to an old manifest alone would misclassify a grown/aged excluded file as deleted.
func selectedOldEntry(p Plan, src Source, old Entry, selector Selection) bool {
	if !selector.Possible(old.Path) {
		return false
	}
	for _, excluded := range p.RuntimeExcludes {
		if Contains(excluded, filepath.Join(src.Path, filepath.FromSlash(old.Path))) {
			return false
		}
	}
	var err error
	kind, size, mtime := old.Kind, old.Size, time.Unix(0, old.ModTime)
	path := filepath.Join(src.Path, filepath.FromSlash(old.Path))
	if err = SafeTree(src.Path, filepath.Dir(path)); err != nil {
		return false
	}
	st, err := os.Lstat(path)
	if err == nil {
		kind, size, mtime = entryKind(st), st.Size(), st.ModTime()
	} else if !os.IsNotExist(err) {
		return false
	}
	ok, _ := selector.Match(old.Path, kind, size, mtime)
	return ok
}

// ExplainSelection provides the same compiled decision used by scanning, without
// re-reading or following source paths when rendering the rules preview.
func ExplainSelection(p Plan, entries []Entry) []Operation {
	selectors := map[string]Selection{}
	for _, src := range p.Sources {
		if selector, err := CompileSelection(p, src); err == nil {
			selectors[src.ID] = selector
		}
	}
	out := make([]Operation, 0, len(entries))
	for _, en := range entries {
		_, reason := selectors[en.SourceID].Match(en.Path, en.Kind, en.Size, time.Unix(0, en.ModTime))
		out = append(out, Operation{SourceID: en.SourceID, Path: en.Path, Type: "selected", Size: en.Size, Reason: reason})
	}
	return out
}
