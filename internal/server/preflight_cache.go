package server

import (
	"context"
	"fmt"
	"os"
	"sync"
	"time"

	"localbackup/internal/backup"
)

const preflightCacheTTL = 10 * time.Minute
const preflightCacheLimit = 64

// Cache only a tool capability probe on generated data, never a source manifest
// or a backup authorization. Real source/target checks run on every preview.
type preflightCache struct {
	mu      sync.Mutex
	success map[string]time.Time
}

func (c *preflightCache) contains(key string, now time.Time) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	expires, ok := c.success[key]
	return ok && now.Before(expires)
}
func (c *preflightCache) remember(key string, now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.success == nil {
		c.success = make(map[string]time.Time)
	}
	for k, expires := range c.success {
		if !now.Before(expires) {
			delete(c.success, k)
		}
	}
	if len(c.success) >= preflightCacheLimit {
		var oldest string
		var expiry time.Time
		for k, expires := range c.success {
			if oldest == "" || expires.Before(expiry) {
				oldest, expiry = k, expires
			}
		}
		delete(c.success, oldest)
	}
	c.success[key] = now.Add(preflightCacheTTL)
}
func rsyncProbeKey(rs backup.RsyncInfo) (string, error) {
	real, err := backup.Canonical(rs.Path)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(real)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0111 == 0 {
		return "", fmt.Errorf("rsync 程序不是可执行的普通文件")
	}
	return fingerprint(struct {
		Version              string
		Rsync                backup.RsyncInfo
		Executable, Metadata string
		Size, Mtime          int64
		Mode                 uint32
	}{"rsync-capabilities-v1", rs, real, backup.MetadataVersion(info), info.Size(), info.ModTime().UnixNano(), uint32(info.Mode())}), nil
}
func (s *Server) preflight(ctx context.Context, p backup.Plan, d backup.Destination, entries []backup.Entry, previous *backup.Snapshot, rs backup.RsyncInfo, lightweight bool) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	// Execution always performs a fresh full-manifest dry run before transfer.
	if !lightweight {
		return false, backup.DryRun(ctx, p, d, entries, previous, rs)
	}
	if !rs.Available {
		return false, fmt.Errorf("rsync 不可用")
	}
	key, err := rsyncProbeKey(rs)
	if err != nil {
		return false, err
	}
	if s.preflightCache.contains(key, time.Now()) {
		return true, ctx.Err()
	}
	// This bounded probe never receives any user source, target or manifest.
	if err = backup.CheckRsyncCapabilities(ctx, rs); err != nil {
		return false, err
	}
	if err = ctx.Err(); err != nil {
		return false, err
	}
	after, err := rsyncProbeKey(rs)
	if err != nil {
		return false, err
	}
	if after != key {
		return false, fmt.Errorf("预检查期间 rsync 程序已变化，请重新预览")
	}
	s.preflightCache.remember(key, time.Now())
	return false, nil
}
