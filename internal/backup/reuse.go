package backup

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

// Link metadata-eligible candidates into private staging. Their contents are
// verified by the mandatory final staging SHA-256 pass, not redundantly read
// here and then read again through the same inode. Source content is still
// checked independently. A corrupt candidate fails final verification and is
// never published. Sync modes also retain their full pre-write baseline audit.
// These paths are absent from rsync's allowlist, so rsync cannot update any
// contents or attributes of an inode shared with immutable history.
func reuseSelectedFiles(ctx context.Context, p Plan, stage string, entries []Entry, previous *Snapshot, modern bool) (map[string]bool, error) {
	reused := map[string]bool{}
	if previous == nil {
		return reused, nil
	}
	old := make(map[string]Entry, len(previous.Entries))
	for _, en := range previous.Entries {
		old[entryKey(en)] = en
	}
	moves := map[string]string{}
	for _, op := range Compare(entries, previous.Entries, "").Operations {
		if op.Type == "moved" {
			moves[op.SourceID+"/"+op.Path] = op.OldPath
		}
	}
	sources := map[string]Source{}
	for _, src := range p.Sources {
		if src.Enabled {
			sources[src.ID] = src
		}
	}
	for _, en := range entries {
		if err := Checkpoint(ctx); err != nil {
			return nil, err
		}
		if en.Kind != "file" {
			continue
		}
		prior, ok := old[entryKey(en)]
		if !ok {
			if moved, exists := moves[entryKey(en)]; exists {
				prior, ok = old[en.SourceID+"/"+moved]
			}
		}
		if !ok || !sameMetadata(en, prior) {
			continue
		}
		from := filepath.Join(previous.Path, prior.SourceID, filepath.FromSlash(prior.Path))
		if err := SafeTree(previous.Path, filepath.Dir(from)); err != nil {
			return nil, err
		}
		st, err := os.Lstat(from)
		if err != nil || !st.Mode().IsRegular() || st.Size() != en.Size || uint32(st.Mode().Perm()) != en.Mode || st.ModTime().Unix() != en.ModTime/1e9 {
			continue
		}
		if modern {
			attrs, err := AttributeHash(from)
			if err != nil || attrs != en.AttributeHash {
				continue
			}
		}
		src, ok := sources[en.SourceID]
		if !ok {
			return nil, fmt.Errorf("复用源范围无效")
		}
		if err = verifySourceEntry(ctx, src, en); err != nil {
			return nil, err
		}
		to := filepath.Join(stage, en.SourceID, filepath.FromSlash(en.Path))
		if err = SafeTree(stage, filepath.Dir(to)); err != nil {
			return nil, err
		}
		if err = os.Link(from, to); err != nil {
			if errors.Is(err, unix.EXDEV) || errors.Is(err, unix.ENOTSUP) || errors.Is(err, unix.EPERM) {
				continue
			}
			return nil, err
		}
		linked, err := os.Lstat(to)
		if err != nil || !linked.Mode().IsRegular() || !os.SameFile(st, linked) {
			return nil, fmt.Errorf("复用期间历史文件身份已变化: %s", en.Path)
		}
		reused[entryKey(en)] = true
	}
	return reused, nil
}
