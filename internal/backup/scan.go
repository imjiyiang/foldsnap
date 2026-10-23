package backup

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"golang.org/x/sys/unix"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

type compiledRule struct {
	rule Rule
	re   *regexp.Regexp
}

func CompileRules(rules []Rule) ([]compiledRule, error) {
	out := []compiledRule{}
	for _, r := range rules {
		if r.Action != "include" && r.Action != "exclude" {
			return nil, errors.New("规则操作只能是 include 或 exclude")
		}
		if r.Pattern == "" || strings.ContainsAny(r.Pattern, "\x00\r\n") {
			return nil, errors.New("规则不能为空或包含换行")
		}
		p := strings.TrimPrefix(filepath.ToSlash(r.Pattern), "/")
		var b strings.Builder
		anchored := strings.Contains(p, "/")
		p = strings.TrimSuffix(p, "/**")
		if anchored {
			b.WriteString("^")
		} else {
			b.WriteString("(^|/)")
		}
		for i := 0; i < len(p); i++ {
			switch p[i] {
			case '*':
				if i+1 < len(p) && p[i+1] == '*' {
					i++
					if i+1 < len(p) && p[i+1] == '/' {
						i++
						b.WriteString("(?:.*/)?")
					} else {
						b.WriteString(".*")
					}
				} else {
					b.WriteString("[^/]*")
				}
			case '?':
				b.WriteString("[^/]")
			default:
				b.WriteString(regexp.QuoteMeta(string(p[i])))
			}
		}
		b.WriteString("(/.*)?$")
		re, e := regexp.Compile(b.String())
		if e != nil {
			return nil, e
		}
		out = append(out, compiledRule{r, re})
	}
	return out, nil
}
func MatchRules(path string, hidden bool, rules []compiledRule) (bool, string) {
	if hidden {
		for _, c := range strings.Split(path, "/") {
			if strings.HasPrefix(c, ".") {
				return false, "隐藏文件"
			}
		}
	}
	for _, r := range rules {
		if r.re.MatchString(path) {
			return r.rule.Action == "include", r.rule.Action + ": " + r.rule.Pattern
		}
	}
	return true, "默认包含"
}

// Each active reader owns its buffer; reuse avoids 256 KiB of allocation per
// small file without changing content verification or cancellation checkpoints.
var hashBuffers = sync.Pool{New: func() any { return new([256 * 1024]byte) }}

func HashFile(ctx context.Context, path string) (string, error) {
	return hashStableFile(ctx, path, nil)
}
func hashStableFile(ctx context.Context, path string, expected os.FileInfo) (string, error) {
	if err := Checkpoint(ctx); err != nil {
		return "", err
	}
	descriptor, e := unix.Open(path, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC|unix.O_NONBLOCK, 0)
	if e != nil {
		return "", e
	}
	f := os.NewFile(uintptr(descriptor), path)
	before, e := f.Stat()
	if e != nil {
		f.Close()
		return "", e
	}
	if !before.Mode().IsRegular() || (expected != nil && !samePhysicalVersion(expected, before)) {
		f.Close()
		return "", fmt.Errorf("文件身份或元数据在读取前变化: %s", path)
	}
	defer f.Close()
	Report(ctx, WorkEvent{Path: path})
	h := sha256.New()
	buffer := hashBuffers.Get().(*[256 * 1024]byte)
	defer hashBuffers.Put(buffer)
	buf := buffer[:]
	for {
		if e = Checkpoint(ctx); e != nil {
			return "", e
		}
		n, er := f.Read(buf)
		if n > 0 {
			h.Write(buf[:n])
			Report(ctx, WorkEvent{Bytes: int64(n)})
		}
		if er == io.EOF {
			break
		}
		if er != nil {
			return "", er
		}
	}
	after, e := f.Stat()
	if e != nil {
		return "", e
	}
	if !samePhysicalVersion(before, after) {
		return "", fmt.Errorf("文件在计算指纹时发生变化: %s", path)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
func Scan(ctx context.Context, p Plan) ([]Entry, []Operation, error) {
	result, err := ScanIndexed(ctx, p, nil, true)
	return result.Entries, result.Ignored, err
}
func ScanIndexed(ctx context.Context, p Plan, index FingerprintIndex, force bool) (ScanResult, error) {
	result, err := scanIndexed(ctx, p, index, force)
	if errors.Is(err, ErrIndexStale) {
		p.RuntimeDirtyRoots = nil
		return scanIndexed(ctx, p, index, force)
	}
	return result, err
}
func scanIndexed(ctx context.Context, p Plan, index FingerprintIndex, force bool) (ScanResult, error) {
	stats := ScanStats{Forced: force}
	p.EvaluatedAt = EvaluationTime(p)
	entries := []Entry{}
	ignored := []Operation{}
	for _, s := range p.Sources {
		if !s.Enabled {
			continue
		}
		identityStart := time.Now()
		var resolveErr error
		s, resolveErr = ResolveSource(s)
		if resolveErr != nil {
			return ScanResult{}, resolveErr
		}
		root, e := Canonical(s.Path)
		if e != nil || root != s.Path {
			return ScanResult{}, fmt.Errorf("源目录 %s 不可用或路径已改变", s.Name)
		}
		for _, excluded := range p.RuntimeExcludes {
			if Contains(excluded, root) {
				return ScanResult{}, fmt.Errorf("源目录 %s 位于服务数据目录内", s.Name)
			}
		}
		rootStat, rootErr := os.Lstat(root)
		if rootErr != nil || !rootStat.IsDir() {
			return ScanResult{}, fmt.Errorf("源目录类型已变化或不可访问: %s", s.Name)
		}
		stats.Timings.IdentityMS += float64(time.Since(identityStart)) / float64(time.Millisecond)
		indexStart := time.Now()
		scope := SourceScope{SourceID: s.ID, Root: root, ScannedAt: time.Now().UTC()}
		hashes := map[string]string{}
		observed := []IndexedPath{}
		if index != nil {
			scope.Generation, e = index.BeginSource(ctx, s.ID)
			if e != nil {
				return ScanResult{}, e
			}
			scope.VolumeID, _ = VolumeID(root)
			scope.Filesystem, scope.Cacheable = cacheFilesystem(root)
			if scope.VolumeID == "" {
				scope.Cacheable = false
			}
			rootEntry := Entry{}
			populateIdentity(&rootEntry, rootStat, scope.VolumeID)
			scope.RootIdentity = rootEntry.Identity
			policy, _ := json.Marshal(struct {
				Source   Source
				Rules    []Rule
				Filters  FileFilters
				Excludes []string
			}{s, p.Rules, p.FileFilters, p.RuntimeExcludes})
			scope.Policy = string(policy)
			if !force && scope.Cacheable && scope.VolumeID != "" {
				hashes, e = index.SourceHashes(ctx, s.ID)
				if e != nil {
					return ScanResult{}, e
				}
			}
		}
		stats.Timings.IndexReadMS += float64(time.Since(indexStart)) / float64(time.Millisecond)
		walkStart := time.Now()
		type deviceScope struct {
			volume    string
			cacheable bool
		}
		deviceScopes := map[string]deviceScope{identityDevice(rootStat): {scope.VolumeID, scope.Cacheable}}
		selector, e := CompileSelection(p, s)
		if e != nil {
			return ScanResult{}, e
		}
		scanRoots := []string{"."}
		var reused []IndexedPath
		if hints, eligible := p.RuntimeDirtyRoots[s.ID]; eligible && !force && selector.filters.ModifiedWithinDays == nil {
			if projections, ok := index.(ProjectionIndex); ok {
				base, err := projections.LoadSourceProjection(ctx, s.ID)
				if err != nil {
					return ScanResult{}, err
				}
				if reusableProjection(scope, base) {
					parents, ok, err := dirtyParents(root, hints, selector)
					if err != nil {
						return ScanResult{}, err
					}
					if ok {
						scope.Partial = true
						scope.BaseGeneration = base.Scope.Generation
						scope.FullScanAt = base.Scope.FullScanAt
						scanRoots = parents
						for _, record := range base.Paths {
							if !underScan(record.Entry.Path, parents) {
								reused = append(reused, record)
							}
						}
					}
				}
			}
		}
		if scope.Partial {
			stats.SubtreeSources++
		} else {
			stats.FullSources++
		}
		matcher := newWalkSelection(selector)
		visited := 0
		visit := func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if er := Checkpoint(ctx); er != nil {
				return er
			}
			if path == root {
				return nil
			}
			visited++
			stats.PathsVisited++
			for _, excluded := range p.RuntimeExcludes {
				if Contains(excluded, path) {
					ignored = append(ignored, Operation{SourceID: s.ID, Path: path, Type: "ignored", Reason: "LocalBackup 服务数据目录"})
					if d.IsDir() {
						return filepath.SkipDir
					}
					return nil
				}
			}
			rel, er := filepath.Rel(root, path)
			if er != nil {
				return er
			}
			rel = filepath.ToSlash(rel)
			statStart := time.Now()
			st, er := d.Info()
			stats.Timings.InitialStatMS += float64(time.Since(statStart)) / float64(time.Millisecond)
			if er != nil {
				return er
			}
			if _, exact := selector.exceptions[rel]; exact && st.IsDir() {
				return fmt.Errorf("单文件例外指向了文件夹: %s", rel)
			}
			en := Entry{SourceID: s.ID, Path: rel, Kind: entryKind(st), Size: st.Size(), ModTime: st.ModTime().UnixNano(), Mode: uint32(st.Mode().Perm())}
			device := identityDevice(st)
			fileScope, known := deviceScopes[device]
			if !known && index != nil {
				volume, _ := VolumeID(path)
				_, cacheable := cacheFilesystem(path)
				fileScope = deviceScope{volume, cacheable && volume != ""}
				deviceScopes[device] = fileScope
			}
			populateIdentity(&en, st, fileScope.volume)
			if en.Kind == "directory" {
				en.Size = 0
			}
			selectionStart := time.Now()
			include, reason := matcher.Match(rel, en.Kind, en.Size, st.ModTime())
			stats.Timings.SelectionMS += float64(time.Since(selectionStart)) / float64(time.Millisecond)
			if !include {
				ignored = append(ignored, Operation{SourceID: s.ID, Path: rel, Type: "ignored", Size: en.Size, Reason: reason})
				observed = append(observed, IndexedPath{Entry: en, State: "excluded", Reason: reason})
				if d.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
			attributeStart := time.Now()
			en.AttributeHash, er = AttributeHash(path)
			stats.Timings.AttributesMS += float64(time.Since(attributeStart)) / float64(time.Millisecond)
			if er != nil {
				return er
			}
			fingerprintStart := time.Now()
			wasHashed := false
			switch en.Kind {
			case "file":
				key := ContentKey(en)
				hash := ""
				if index != nil && !force && fileScope.cacheable && key != "" {
					hash = hashes[key]
					if hash == "" {
						hash, er = index.LookupHash(ctx, key)
						if er != nil {
							return er
						}
					}
					// The final Lstat below validates the physical version after
					// attributes/cache lookup for every selected entry. Do not make
					// a second, adjacent Lstat just for cache hits.
					if !validHash(hash) {
						hash = ""
					}
				}
				if hash == "" {
					hash, er = hashStableFile(ctx, path, st)
					if er != nil {
						return er
					}
					wasHashed = true
					stats.FilesHashed++
					stats.BytesHashed += en.Size
				} else {
					Report(ctx, WorkEvent{Path: path, CacheHit: true, CacheBytes: en.Size})
					stats.CacheHits++
					stats.BytesReused += en.Size
				}
				en.Hash = hash
				if key != "" {
					hashes[key] = hash
				}
			case "symlink":
				en.Link, er = os.Readlink(path)
			case "directory":
			default:
				return fmt.Errorf("不支持特殊文件: %s", path)
			}
			if er != nil {
				return er
			}
			stats.Timings.FingerprintMS += float64(time.Since(fingerprintStart)) / float64(time.Millisecond)
			// A cache hit is useful only if the selected pathname still names
			// the metadata version observed before reading attributes/content.
			statStart = time.Now()
			latest, err := os.Lstat(path)
			stats.Timings.FinalStatMS += float64(time.Since(statStart)) / float64(time.Millisecond)
			if err != nil {
				return err
			}
			if !samePhysicalVersion(st, latest) {
				return fmt.Errorf("扫描期间文件发生变化: %s", rel)
			}
			observed = append(observed, IndexedPath{Entry: en, State: "selected", Reason: reason, Hashed: wasHashed, Cacheable: fileScope.cacheable})
			entries = append(entries, en)
			return nil
		}
		for _, relative := range scanRoots {
			start := root
			if relative != "." {
				start = filepath.Join(root, filepath.FromSlash(relative))
			}
			if _, err := os.Lstat(start); os.IsNotExist(err) {
				continue
			} else if err != nil {
				return ScanResult{}, err
			}
			if e = walkSourceRead(ctx, start, visit, func(ctx context.Context, path string) ([]os.DirEntry, error) {
				start := time.Now()
				children, err := ReadDirectory(ctx, path)
				stats.Timings.DirectoryReadMS += float64(time.Since(start)) / float64(time.Millisecond)
				return children, err
			}); e != nil {
				return ScanResult{}, e
			}
		}
		// Refresh ancestor metadata without enumerating unrelated siblings.
		if scope.Partial {
			refreshed := map[string]bool{}
			for _, relative := range scanRoots {
				for parent := filepath.ToSlash(filepath.Dir(relative)); parent != "."; parent = filepath.ToSlash(filepath.Dir(parent)) {
					if refreshed[parent] || underScan(parent, scanRoots) {
						continue
					}
					refreshed[parent] = true
					path := filepath.Join(root, filepath.FromSlash(parent))
					st, err := os.Lstat(path)
					if err != nil {
						return ScanResult{}, err
					}
					if !st.IsDir() {
						return ScanResult{}, ErrIndexStale
					}
					if e = visit(path, fs.FileInfoToDirEntry(st), nil); e != nil {
						if e == filepath.SkipDir {
							return ScanResult{}, ErrIndexStale
						}
						return ScanResult{}, e
					}
				}
			}
			for _, record := range reused {
				if refreshed[record.Entry.Path] {
					continue
				}
				stats.ProjectedPaths++
				observed = append(observed, record)
				if record.State == "selected" {
					entries = append(entries, record.Entry)
				} else {
					ignored = append(ignored, Operation{SourceID: s.ID, Path: record.Entry.Path, Type: "ignored", Size: record.Entry.Size, Reason: record.Reason})
				}
			}
		}
		validationStart := time.Now()
		scope.WalkedPaths = visited
		latestRoot, err := os.Lstat(root)
		if err != nil || !latestRoot.IsDir() {
			return ScanResult{}, fmt.Errorf("源目录在扫描期间消失: %s", s.Name)
		}
		previousRoot, nowRoot := Entry{}, Entry{}
		populateIdentity(&previousRoot, rootStat, scope.VolumeID)
		populateIdentity(&nowRoot, latestRoot, scope.VolumeID)
		if previousRoot.Identity != nowRoot.Identity {
			return ScanResult{}, fmt.Errorf("源目录身份在扫描期间变化: %s", s.Name)
		}
		if _, err := BindSource(s); err != nil {
			return ScanResult{}, err
		}
		stats.Timings.SourceValidationMS += float64(time.Since(validationStart)) / float64(time.Millisecond)
		stats.Timings.WalkMS += float64(time.Since(walkStart)) / float64(time.Millisecond)
		if index != nil {
			commitStart := time.Now()
			if e = index.CommitSource(ctx, scope, observed); e != nil {
				return ScanResult{}, e
			}
			stats.Timings.IndexCommitMS += float64(time.Since(commitStart)) / float64(time.Millisecond)
		}
	}
	sortStart := time.Now()
	sort.Slice(entries, func(i, j int) bool { return entryKey(entries[i]) < entryKey(entries[j]) })
	sort.Slice(ignored, func(i, j int) bool {
		return ignored[i].SourceID+"/"+ignored[i].Path < ignored[j].SourceID+"/"+ignored[j].Path
	})
	stats.Timings.SortMS += float64(time.Since(sortStart)) / float64(time.Millisecond)
	return ScanResult{entries, ignored, stats}, nil
}
func entryKey(e Entry) string { return e.SourceID + "/" + e.Path }
func sameContent(a, b Entry) bool {
	return a.Kind == b.Kind && a.Hash == b.Hash && a.Link == b.Link && a.Size == b.Size
}
func sameMetadata(a, b Entry) bool {
	return sameContent(a, b) && (a.BasicMetadata || b.BasicMetadata || a.AttributeHash == b.AttributeHash) && a.Mode == b.Mode && a.ModTime/1e9 == b.ModTime/1e9
}
func Compare(entries []Entry, old []Entry, dest string) Preview {
	v := Preview{Operations: []Operation{}, Warnings: []string{}}
	before := map[string]Entry{}
	now := map[string]bool{}
	candidates := map[string][]Entry{}
	for _, e := range entries {
		now[entryKey(e)] = true
	}
	for _, e := range old {
		before[entryKey(e)] = e
		if !now[entryKey(e)] && e.Kind == "file" {
			k := e.SourceID + ":" + e.Hash
			candidates[k] = append(candidates[k], e)
		}
	}
	moved := map[string]bool{}
	for _, e := range entries {
		if e.Kind == "directory" {
			continue
		}
		v.Files++
		v.Bytes += e.Size
		o := Operation{SourceID: e.SourceID, DestinationID: dest, Path: e.Path, Size: e.Size}
		a, ok := before[entryKey(e)]
		if ok && sameMetadata(e, a) {
			v.Reused++
			continue
		}
		if ok {
			o.Type = "modified"
			v.Modified++
			v.TransferBytes += e.Size
		} else {
			list := candidates[e.SourceID+":"+e.Hash]
			var match *Entry
			for _, c := range list {
				if !moved[entryKey(c)] && sameMetadata(c, e) {
					cc := c
					match = &cc
					break
				}
			}
			if match != nil {
				o.Type = "moved"
				o.OldPath = match.Path
				moved[entryKey(*match)] = true
				v.Moved++
			} else {
				o.Type = "added"
				v.Added++
				v.TransferBytes += e.Size
			}
		}
		v.Operations = append(v.Operations, o)
	}
	for _, a := range old {
		if a.Kind != "directory" && !now[entryKey(a)] && !moved[entryKey(a)] {
			v.Deleted++
			v.Operations = append(v.Operations, Operation{SourceID: a.SourceID, DestinationID: dest, Path: a.Path, Type: "deleted", Size: a.Size, Reason: "仅从新快照中移除，历史版本保留"})
		}
	}
	return v
}
