package backup

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"golang.org/x/sys/unix"
	"io"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// A receipt locates disposable work; it is never publication authority. Every
// reused file is compared with freshly scanned entries and the current source.
type stagingReceipt struct {
	Version    int       `json:"version"`
	SnapshotID string    `json:"snapshotId"`
	Binding    string    `json:"binding"`
	CreatedAt  time.Time `json:"createdAt"`
}

func stagingBinding(p Plan, d Destination, repository string, modern bool) string {
	b, _ := json.Marshal(struct {
		PlanID, Mode, Repository string
		Destination              Destination
		Sources                  []Source
		Rules                    []Rule
		Filters                  FileFilters
		Excludes                 []string
		Modern                   bool
	}{p.ID, p.Mode, repository, d, p.Sources, p.Rules, p.FileFilters, p.RuntimeExcludes, modern})
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func receiptPath(staging, id string) string { return filepath.Join(staging, id+".resume.json") }

func saveStagingReceipt(staging, id, binding string) error {
	b, err := json.Marshal(stagingReceipt{1, id, binding, time.Now().UTC()})
	if err != nil {
		return err
	}
	if err = writeAtomic(receiptPath(staging, id), b); err != nil {
		return err
	}
	f, err := os.Open(staging)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}

func readStagingReceipt(staging, id, binding string) (stagingReceipt, bool) {
	var r stagingReceipt
	path := receiptPath(staging, id)
	if SafeTree(staging, path) != nil {
		return r, false
	}
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC|unix.O_NONBLOCK, 0)
	if err != nil {
		return r, false
	}
	f := os.NewFile(uintptr(fd), path)
	defer f.Close()
	st, err := f.Stat()
	if err != nil || !st.Mode().IsRegular() || st.Size() > 4096 {
		return r, false
	}
	b, err := io.ReadAll(io.LimitReader(f, 4097))
	if err != nil || len(b) > 4096 || json.Unmarshal(b, &r) != nil {
		return r, false
	}
	return r, r.Version == 1 && r.SnapshotID == id && r.Binding == binding && !r.CreatedAt.IsZero()
}

// Limit historical candidate work. A bad/old receipt only costs an optimization;
// no arbitrary files are imported and no previous tree is modified or deleted.
func recoverStagedFiles(ctx context.Context, staging, stage, binding string, p Plan, entries []Entry, modern bool, occupied map[string]bool, log func(string)) (map[string]bool, error) {
	recovered := map[string]bool{}
	var recoveredBytes int64
	dirs, err := os.ReadDir(staging)
	if err != nil {
		return nil, err
	}
	type candidate struct {
		path string
		at   time.Time
	}
	candidates := []candidate{}
	for _, dir := range dirs {
		if err = Checkpoint(ctx); err != nil {
			return nil, err
		}
		if !dir.IsDir() || !ValidID(dir.Name()) || dir.Name() == filepath.Base(stage) {
			continue
		}
		if r, ok := readStagingReceipt(staging, dir.Name(), binding); ok {
			candidates = append(candidates, candidate{filepath.Join(staging, dir.Name()), r.CreatedAt})
		}
	}
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].at.After(candidates[j].at) })
	if len(candidates) > 8 {
		candidates = candidates[:8]
	}
	sources := map[string]Source{}
	for _, src := range p.Sources {
		if src.Enabled {
			sources[src.ID] = src
		}
	}
	for _, old := range candidates {
		if err = SafeTree(staging, old.path); err != nil {
			continue
		}
		for _, en := range entries {
			if err = Checkpoint(ctx); err != nil {
				return nil, err
			}
			if en.Kind != "file" || recovered[entryKey(en)] || occupied[entryKey(en)] {
				continue
			}
			src, ok := sources[en.SourceID]
			if !ok || !SafeRelative(en.Path) {
				continue
			}
			from := filepath.Join(old.path, en.SourceID, filepath.FromSlash(en.Path))
			if SafeTree(old.path, from) != nil {
				continue
			}
			st, er := os.Lstat(from)
			if er != nil || !st.Mode().IsRegular() || st.Size() != en.Size || uint32(st.Mode().Perm()) != en.Mode || st.ModTime().Unix() != en.ModTime/1e9 {
				continue
			}
			hash, er := hashStableFile(ctx, from, st)
			if er != nil {
				if ctx.Err() != nil {
					return nil, ctx.Err()
				}
				continue
			}
			if hash != en.Hash {
				continue
			}
			if modern {
				attrs, er := AttributeHash(from)
				if er != nil || attrs != en.AttributeHash {
					continue
				}
			}
			current := filepath.Join(src.Path, filepath.FromSlash(en.Path))
			if er = SafeTree(src.Path, filepath.Dir(current)); er != nil {
				return nil, er
			}
			info, er := os.Lstat(current)
			if er != nil {
				return nil, er
			}
			if !info.Mode().IsRegular() || info.Size() != en.Size || info.ModTime().UnixNano() != en.ModTime || uint32(info.Mode().Perm()) != en.Mode {
				continue
			}
			if modern {
				attrs, er := AttributeHash(current)
				if er != nil || attrs != en.AttributeHash {
					continue
				}
			}
			actual, er := hashStableFile(ctx, current, info)
			if er != nil {
				return nil, er
			}
			if actual != en.Hash {
				continue
			}
			to := filepath.Join(stage, en.SourceID, filepath.FromSlash(en.Path))
			// Clone or copy into an independent inode. Never hardlink mutable
			// interrupted work into a new committed snapshot.
			if !cloneStagedFile(from, to) {
				if er = copyRetainedFile(ctx, from, to, en, modern); er != nil {
					return nil, er
				}
			}
			recovered[entryKey(en)] = true
			recoveredBytes += en.Size
		}
	}
	if len(recovered) > 0 {
		log(fmt.Sprintf("已校验并恢复暂存文件: %d 个，%d B", len(recovered), recoveredBytes))
	}
	return recovered, nil
}
