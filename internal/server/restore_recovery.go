package server

import (
	"encoding/json"
	"log"
	"net/http"
	"time"
)

// Only restoration records are reclassified. Directory creation and config
// recovery share this audit table but have separate operation semantics.
func (s *Server) recoverRestoreJobs() {
	_, err := s.Store.DB.Exec(`UPDATE restore_jobs SET data=json_set(data,
 '$.status','interrupted', '$.finishedAt', ?, '$.error', ?)
 WHERE json_extract(data,'$.status')='running'
 AND (json_extract(data,'$.type')='restore' OR json_type(data,'$.request.snapshotId')='text')`,
		time.Now().UTC().Format(time.RFC3339Nano),
		"服务重启，恢复任务中断；输出目录可能不存在或仅包含部分文件。请检查后重新恢复到新目录，原备份未修改。")
	if err != nil {
		log.Printf("恢复任务中断状态写入失败: %v", err)
	}
}

func (s *Server) listRestores(w http.ResponseWriter, r *http.Request) {
	rows, err := s.Store.DB.QueryContext(r.Context(), `SELECT data FROM restore_jobs WHERE json_extract(data,'$.type')='restore' OR json_type(data,'$.request.snapshotId')='text' ORDER BY rowid DESC LIMIT 50`)
	if err != nil {
		fail(w, 500, err.Error())
		return
	}
	defer rows.Close()
	out := []json.RawMessage{}
	for rows.Next() {
		var data string
		if err := rows.Scan(&data); err != nil {
			fail(w, 500, err.Error())
			return
		}
		out = append(out, json.RawMessage(data))
	}
	if err := rows.Err(); err != nil {
		fail(w, 500, err.Error())
		return
	}
	reply(w, 200, out)
}
