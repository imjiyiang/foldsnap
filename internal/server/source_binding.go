package server

import (
	"errors"
	"localbackup/internal/backup"
	"path/filepath"
)

func allSourcesBound(p backup.Plan) bool {
	for _, src := range p.Sources {
		if !backup.SourceBound(src) {
			return false
		}
	}
	return true
}
func (s *Server) resolveSources(p backup.Plan) (backup.Plan, error) {
	p, err := backup.ResolveSources(p)
	if err != nil {
		return p, err
	}
	plans, err := s.Store.Plans()
	if err != nil {
		return p, err
	}
	roots := []string{}
	seen := map[string]bool{}
	for _, other := range plans {
		for _, d := range other.Destinations {
			if seen[d.Path] {
				continue
			}
			seen[d.Path] = true
			if root, err := backup.CheckDestination(d); err == nil {
				roots = append(roots, filepath.Dir(root))
			}
		}
	}
	for _, src := range p.Sources {
		if !src.Enabled {
			continue
		}
		for _, root := range roots {
			if backup.PathsOverlap(src.Path, root) {
				return p, errors.New("源目录重新定位后与已有备份目标重叠")
			}
		}
	}
	return p, nil
}
