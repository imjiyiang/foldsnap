package server

import (
	"context"
	"errors"
	"golang.org/x/sys/unix"
	"io"
	"localbackup/internal/backup"
	"os"
	"path/filepath"
	"runtime"
	"time"
)

type setupItem struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Path    string `json:"path"`
	State   string `json:"state"`
	Message string `json:"message"`
}
type setupPlan struct {
	ID           string      `json:"id"`
	Name         string      `json:"name"`
	Enabled      bool        `json:"enabled"`
	Ready        bool        `json:"ready"`
	Sources      []setupItem `json:"sources"`
	Destinations []setupItem `json:"destinations"`
	Snapshots    int         `json:"snapshots"`
}
type setupReport struct {
	Snapshots     int              `json:"snapshots"`
	CheckedAt     time.Time        `json:"checkedAt"`
	Platform      string           `json:"platform"`
	DatabaseReady bool             `json:"databaseReady"`
	Rsync         backup.RsyncInfo `json:"rsync"`
	Plans         []setupPlan      `json:"plans"`
}

// Directory access preflight never reads file contents or creates target files.
// The real preview and transfer remain the authority on actual permissions.
func probeDirectory(path string) error {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return err
	}
	file := os.NewFile(uintptr(fd), path)
	defer file.Close()
	_, err = file.ReadDir(1)
	if err == io.EOF {
		return nil
	}
	return err
}
func (s *Server) setup(ctx context.Context) (setupReport, error) {
	report := setupReport{CheckedAt: time.Now().UTC(), Platform: runtime.GOOS, Rsync: backup.DetectRsync(s.config().RsyncPath), Plans: []setupPlan{}}
	if err := s.Store.DB.PingContext(ctx); err != nil {
		return report, err
	}
	report.DatabaseReady = true
	plans, err := s.Store.Plans()
	if err != nil {
		return report, err
	}
	counts := map[string]int{}
	rows, err := s.Store.DB.QueryContext(ctx, "SELECT json_extract(data,'$.planId'),COUNT(*) FROM snapshots GROUP BY json_extract(data,'$.planId')")
	if err != nil {
		return report, err
	}
	for rows.Next() {
		var id string
		var count int
		if err = rows.Scan(&id, &count); err != nil {
			rows.Close()
			return report, err
		}
		counts[id] = count
		report.Snapshots += count
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return report, err
	}
	for _, p := range plans {
		if err := ctx.Err(); err != nil {
			return report, err
		}
		row := setupPlan{ID: p.ID, Name: p.Name, Enabled: p.Enabled, Ready: true, Sources: []setupItem{}, Destinations: []setupItem{}, Snapshots: counts[p.ID]}
		sources, targets := 0, 0
		for _, src := range p.Sources {
			if !src.Enabled {
				continue
			}
			sources++
			item := setupItem{ID: src.ID, Name: src.Name, Path: src.Path, State: "ready", Message: "目录身份与读取预检查通过"}
			resolved, err := backup.ResolveSource(src)
			switch {
			case errors.Is(err, backup.ErrSourceUnbound):
				item.State = "unbound"
				item.Message = "请编辑并保存计划，确认当前源目录身份"
			case err != nil:
				item.State = "unavailable"
				item.Message = err.Error()
			default:
				item.Path = resolved.Path
				if err := probeDirectory(resolved.Path); err != nil {
					item.State = "unreadable"
					item.Message = "无法读取此目录，请检查所选目录的访问权限后重试"
				}
			}
			if item.State != "ready" {
				row.Ready = false
			}
			row.Sources = append(row.Sources, item)
		}
		for _, d := range p.Destinations {
			if !d.Enabled {
				continue
			}
			targets++
			item := setupItem{ID: d.ID, Name: d.Name, Path: d.Path, State: "ready", Message: "已识别绑定磁盘，写入权限预检查通过"}
			root, err := backup.CheckDestination(d)
			if err != nil {
				item.State = "unavailable"
				item.Message = "请连接已绑定的原目标磁盘"
			} else {
				item.Path = filepath.Dir(root)
				if err := unix.Access(item.Path, unix.W_OK); err != nil {
					item.State = "unwritable"
					item.Message = "目标目录写入权限预检查未通过，请检查磁盘或目录权限"
				}
			}
			if item.State != "ready" {
				row.Ready = false
			}
			row.Destinations = append(row.Destinations, item)
		}
		if sources == 0 || targets == 0 {
			row.Ready = false
		}
		report.Plans = append(report.Plans, row)
	}
	return report, nil
}
