package server

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"localbackup/internal/backup"
)

type recoveryResult struct {
	Imported int      `json:"imported"`
	Warnings []string `json:"warnings"`
}

func (s *Server) importRepository(ctx context.Context, d backup.Destination, verify bool) (recoveryResult, error) {
	out := recoveryResult{Warnings: []string{}}
	unlock, lockErr := backup.LockRepository(d, false, false)
	if lockErr != nil {
		return out, lockErr
	}
	defer unlock()
	repo, root, e := backup.OpenRepository(d, false)
	if e != nil {
		return out, e
	}
	planDirs, e := os.ReadDir(filepath.Join(root, "plans"))
	if e != nil {
		return out, e
	}
	for _, plan := range planDirs {
		if !plan.IsDir() || !backup.ValidID(plan.Name()) {
			continue
		}
		base := filepath.Join(root, "plans", plan.Name(), "snapshots")
		if e = backup.SafeTree(root, base); e != nil {
			out.Warnings = append(out.Warnings, e.Error())
			continue
		}
		dirs, _ := os.ReadDir(base)
		for _, dir := range dirs {
			if e = ctx.Err(); e != nil {
				return out, e
			}
			if !dir.IsDir() || !backup.ValidID(dir.Name()) {
				continue
			}
			path := filepath.Join(base, dir.Name())
			// Already indexed immutable snapshots need no startup reread. Restore
			// and retention still validate the repository and manifest when used.
			if !verify {
				var indexed backup.Snapshot
				if s.Store.Get("snapshots", dir.Name(), &indexed) == nil && indexed.PlanID == plan.Name() && indexed.RepositoryID == repo.ID {
					continue
				}
			}
			if e = backup.SafeTree(root, filepath.Join(path, "manifest.json")); e != nil {
				out.Warnings = append(out.Warnings, e.Error())
				continue
			}
			b, e := backup.ReadMetadataFile(filepath.Join(path, "manifest.json"), 1<<30)
			if e != nil {
				out.Warnings = append(out.Warnings, e.Error())
				continue
			}
			var sn backup.Snapshot
			if e = json.Unmarshal(b, &sn); e != nil {
				out.Warnings = append(out.Warnings, e.Error())
				continue
			}
			if sn.ID != dir.Name() || sn.PlanID != plan.Name() || sn.RepositoryID != repo.ID {
				out.Warnings = append(out.Warnings, "快照清单身份无效: "+dir.Name())
				continue
			}
			actual, e := backup.ValidateSnapshot(sn)
			if e != nil || actual != path {
				out.Warnings = append(out.Warnings, "快照路径无效: "+dir.Name())
				continue
			}
			pinned, known, pinErr := backup.ReadSnapshotPin(root, sn)
			if pinErr != nil {
				out.Warnings = append(out.Warnings, pinErr.Error())
			}
			sn.Pinned = sn.Pinned || pinned || !known || pinErr != nil
			var old backup.Snapshot
			if e = s.Store.Get("snapshots", sn.ID, &old); e == nil {
				continue
			}
			if verify {
				if e = backup.AuditSnapshot(ctx, sn); e != nil {
					out.Warnings = append(out.Warnings, fmt.Sprintf("%s: %s", sn.ID, e))
					continue
				}
			}
			if e = s.Store.Put("snapshots", sn.ID, sn); e != nil {
				return out, e
			}
			out.Imported++
		}
	}
	return out, nil
}
func (s *Server) reconcile(ctx context.Context) {
	start := time.Now()
	watchdog := time.AfterFunc(5*time.Second, func() {
		buf := make([]byte, 1<<20)
		n := runtime.Stack(buf, true)
		// Local diagnostics only; never expose stacks via HTTP or browser logs.
		os.WriteFile(filepath.Join(filepath.Dir(s.Store.Path), "startup-stacks.log"), buf[:n], 0600)
		log.Printf("startup: slow repository check; diagnostic stacks saved locally")
	})
	defer watchdog.Stop()
	defer func() { log.Printf("startup: repository reconciliation completed in %s", time.Since(start)) }()
	plans, e := s.Store.Plans()
	if e != nil {
		return
	}
	snapshots, err := s.Store.Snapshots()
	if err != nil {
		return
	}
	runs, err := s.Store.Runs()
	if err != nil {
		return
	}
	interrupted := map[string]bool{}
	for _, run := range runs {
		if run.Status == "recoverable" {
			interrupted[run.PlanID] = true
		}
	}
	for _, p := range plans {
		for _, d := range p.Destinations {
			// Clean, indexed plans need no removable-disk read on launch.
			// A crash before publication/indexing leaves a recoverable run;
			// a new/recovered database has no indexed snapshot for the target.
			indexed := false
			for _, sn := range snapshots {
				if sn.PlanID == p.ID && sn.DestinationID == d.ID && sn.VolumeID == d.VolumeID {
					indexed = true
					break
				}
			}
			if indexed && !interrupted[p.ID] {
				continue
			}
			if ctx.Err() != nil {
				return
			}
			checkAt := time.Now()
			log.Printf("startup: checking repository %s", d.ID)
			result, err := s.importRepository(ctx, d, false)
			log.Printf("startup: repository %s checked in %s (imported=%d warnings=%d error=%v)", d.ID, time.Since(checkAt), result.Imported, len(result.Warnings), err)
		}
	}
}
