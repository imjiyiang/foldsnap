package server

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/sys/unix"
	"localbackup/internal/backup"
)

func (s *Server) createDirectory(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Parent           backup.Source `json:"parent"`
		Name             string        `json:"name"`
		ProtectedSources []string      `json:"protectedSources"`
	}
	if !decode(w, r, &req) {
		return
	}
	name := strings.TrimSpace(req.Name)
	if name == "" || name == "." || name == ".." || strings.ContainsAny(name, "/\\\x00\r\n") || len(name) > 255 {
		fail(w, 400, "请输入单个文件夹名称，不能包含路径分隔符")
		return
	}
	if !backup.SourceBound(req.Parent) {
		fail(w, 400, "请重新打开目录选择器以确认目录与磁盘身份")
		return
	}
	before, err := os.Lstat(req.Parent.Path)
	if err != nil || !before.IsDir() {
		fail(w, 400, "父目录不存在或已被链接替代")
		return
	}
	parent, err := backup.BindSource(req.Parent)
	if err != nil {
		fail(w, 409, err.Error())
		return
	}
	path := filepath.Join(parent.Path, name)
	protected := []string{filepath.Dir(s.Store.Path)}
	plans, err := s.Store.Plans()
	if err != nil {
		fail(w, 500, err.Error())
		return
	}
	for _, p := range plans {
		for _, source := range p.Sources {
			protected = append(protected, source.Path)
		}
		for _, d := range p.Destinations {
			protected = append(protected, filepath.Join(d.Path, "LocalBackup"))
		}
	}
	protected = append(protected, req.ProtectedSources...)
	for _, root := range protected {
		if resolved, e := backup.Canonical(root); e == nil {
			root = resolved
		}
		if root != "" && (backup.Contains(root, path) || backup.Contains(path, root)) {
			fail(w, 400, "新文件夹不能位于已选源目录、服务数据或备份仓库内部")
			return
		}
	}
	// Also protect repositories not registered in this database.
	for ancestor := parent.Path; ; ancestor = filepath.Dir(ancestor) {
		if filepath.Base(ancestor) == "LocalBackup" {
			if _, e := os.Lstat(filepath.Join(ancestor, "repository.json")); e == nil {
				fail(w, 400, "不能在已有备份仓库内部新建目录")
				return
			}
		}
		if ancestor == filepath.Dir(ancestor) {
			break
		}
	}
	fd, err := unix.Open(parent.Path, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		fail(w, 400, err.Error())
		return
	}
	f := os.NewFile(uintptr(fd), parent.Path)
	defer f.Close()
	opened, err := f.Stat()
	if err != nil || !os.SameFile(before, opened) {
		fail(w, 409, "父目录身份发生变化，请重新选择")
		return
	}
	// Bind once more immediately before mkdirat. The descriptor pins the
	// selected directory instead of following a replacement mounted at its path.
	if _, err = backup.BindSource(parent); err != nil {
		fail(w, 409, err.Error())
		return
	}
	id := backup.ID("mkdir-")
	audit := map[string]any{"id": id, "type": "create-directory", "status": "running", "parent": parent, "path": path, "startedAt": time.Now().UTC()}
	if err = s.Store.Put("restore_jobs", id, audit); err != nil {
		fail(w, 500, err.Error())
		return
	}
	err = unix.Mkdirat(fd, name, 0755)
	audit["finishedAt"] = time.Now().UTC()
	audit["status"] = "success"
	if err != nil {
		audit["status"] = "failed"
		audit["error"] = err.Error()
	}
	if e := s.Store.Put("restore_jobs", id, audit); e != nil {
		fail(w, 500, fmt.Sprintf("目录操作已执行，但无法保存审计：%v", e))
		return
	}
	if err != nil {
		fail(w, 400, "无法新建文件夹（名称可能已存在或没有写入权限）："+err.Error())
		return
	}
	reply(w, 201, map[string]string{"path": path, "auditId": id})
}
