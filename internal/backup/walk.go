package backup

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// An OS directory open cannot always be interrupted. Keep one outstanding call
// per path, bound all outstanding calls, and discard results after cancellation.
var directoryReads = newDirectoryReader(4)

const directoryReadTimeout = 30 * time.Second

var errDirectoryReadPending = errors.New("该目录上一次系统读取仍未返回")

type directoryCall struct {
	done      chan struct{}
	entries   []os.DirEntry
	err       error
	abandoned bool
}

type directoryReader struct {
	mu      sync.Mutex
	pending map[string]*directoryCall
	changed chan struct{}
	limit   int
}

func newDirectoryReader(limit int) *directoryReader {
	return &directoryReader{pending: make(map[string]*directoryCall), changed: make(chan struct{}), limit: limit}
}

// ReadDirectory applies the same bounded wait to source scans and directory pickers.
func ReadDirectory(ctx context.Context, path string) ([]os.DirEntry, error) {
	return directoryReads.read(ctx, path, directoryReadTimeout, os.ReadDir)
}

func (r *directoryReader) read(ctx context.Context, path string, timeout time.Duration, read func(string) ([]os.DirEntry, error)) ([]os.DirEntry, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	path = filepath.Clean(path)
	wait, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var call *directoryCall
	for {
		r.mu.Lock()
		if err := wait.Err(); err != nil {
			r.mu.Unlock()
			return nil, directoryWaitError(ctx, path, err)
		}
		call = r.pending[path]
		if call != nil {
			abandoned := call.abandoned
			r.mu.Unlock()
			if abandoned {
				return nil, fmt.Errorf("%s：%w；重复扫描不会再次发起读取。请待系统文件访问恢复后重试", path, errDirectoryReadPending)
			}
			break
		}
		if len(r.pending) < r.limit {
			call = &directoryCall{done: make(chan struct{})}
			r.pending[path] = call
			r.mu.Unlock()
			go func(c *directoryCall) {
				entries, err := read(path)
				r.mu.Lock()
				c.entries, c.err = entries, err
				delete(r.pending, path)
				close(c.done)
				close(r.changed)
				r.changed = make(chan struct{})
				r.mu.Unlock()
			}(call)
			break
		}
		changed := r.changed
		r.mu.Unlock()
		select {
		case <-changed:
		case <-wait.Done():
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			return nil, fmt.Errorf("等待目录读取资源超时（%s）：其他系统目录读取仍未返回，尚未开始读取此目录: %w", path, wait.Err())
		}
	}
	select {
	case <-wait.Done():
		r.mu.Lock()
		call.abandoned = true
		r.mu.Unlock()
		return nil, directoryWaitError(ctx, path, wait.Err())
	case <-call.done:
		if err := wait.Err(); err != nil {
			return nil, directoryWaitError(ctx, path, err)
		}
		return call.entries, call.err
	}
}
func directoryWaitError(ctx context.Context, path string, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return fmt.Errorf("读取目录超时（%s）：请检查磁盘连接或文件访问状态，然后重新扫描；未完成的扫描不会用于备份: %w", path, err)
}

// Match WalkDir's lexical ordering and SkipDir/SkipAll behavior, with a bounded
// wait at each directory read. Directory symlinks are never traversed.
func walkSource(ctx context.Context, root string, visit fs.WalkDirFunc) error {
	return walkSourceRead(ctx, root, visit, ReadDirectory)
}

func walkSourceRead(ctx context.Context, root string, visit fs.WalkDirFunc, read func(context.Context, string) ([]os.DirEntry, error)) error {
	if err := Checkpoint(ctx); err != nil {
		return err
	}
	info, err := os.Lstat(root)
	if err != nil {
		return visit(root, nil, err)
	}
	var walk func(string, fs.DirEntry) error
	walk = func(path string, d fs.DirEntry) error {
		if err := Checkpoint(ctx); err != nil {
			return err
		}
		if err := visit(path, d, nil); err != nil || !d.IsDir() {
			if err == fs.SkipDir && d.IsDir() {
				return nil
			}
			return err
		}
		children, err := read(ctx, path)
		if err != nil {
			return err
		}
		for _, child := range children {
			if err := walk(filepath.Join(path, child.Name()), child); err != nil {
				if err == fs.SkipDir {
					break
				}
				return err
			}
		}
		return nil
	}
	err = walk(root, fs.FileInfoToDirEntry(info))
	if err == fs.SkipDir || err == fs.SkipAll {
		return nil
	}
	return err
}
