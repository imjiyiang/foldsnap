package server

import "localbackup/internal/backup"

// Status is a readiness probe, not a synchronous subprocess diagnostic.
func (s *Server) cachedRsync() backup.RsyncInfo {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.rsyncInfo
}
func (s *Server) refreshRsync() {
	path := s.config().RsyncPath
	info := backup.DetectRsync(path)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.settings.RsyncPath == path {
		s.rsyncInfo = info
	}
}
