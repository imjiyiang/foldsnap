package server

import (
	"bytes"
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"localbackup/internal/backup"
	"os/exec"
	"runtime"
	"strconv"
	"sync"
	"time"
)

type workMetrics struct {
	CachedFiles    int       `json:"cachedFiles"`
	CachedBytes    int64     `json:"cachedBytes"`
	RunID          string    `json:"runId"`
	Phase          string    `json:"phase"`
	Path           string    `json:"path"`
	Source         string    `json:"source"`
	Destination    string    `json:"destination"`
	Bytes          int64     `json:"bytes"`
	Files          int       `json:"files"`
	BytesPerSecond float64   `json:"bytesPerSecond"`
	FilePercent    *int      `json:"filePercent,omitempty"`
	UpdatedAt      time.Time `json:"updatedAt"`
}
type diskCounter struct {
	ID, Name, Device, Location string
	Read, Write                uint64
}
type diskMetric struct {
	ID       string   `json:"id"`
	Name     string   `json:"name"`
	Device   string   `json:"device"`
	Location string   `json:"location"`
	ReadBPS  *float64 `json:"readBps"`
	WriteBPS *float64 `json:"writeBps"`
}
type monitorSample struct {
	At    time.Time    `json:"at"`
	Disks []diskMetric `json:"disks"`
	Work  workMetrics  `json:"work"`
}
type monitorState struct {
	mu            sync.Mutex
	work          workMetrics
	history       []monitorSample
	previous      map[string]diskCounter
	last          time.Time
	error         string
	lastBytes     int64
	workPhase     string
	diskInfo      map[string]diskCounter
	diskInfoRetry map[string]time.Time
}

func (s *Server) observeRun(id string) func(backup.WorkEvent) {
	s.monitor.mu.Lock()
	s.monitor.work = workMetrics{RunID: id, Phase: "scan", UpdatedAt: time.Now()}
	s.monitor.lastBytes = 0
	s.monitor.mu.Unlock()
	return func(e backup.WorkEvent) {
		m := &s.monitor
		m.mu.Lock()
		defer m.mu.Unlock()
		w := &m.work
		if e.Phase != "" && e.Phase != w.Phase {
			w.Phase = e.Phase
			w.Bytes = 0
			w.CachedFiles = 0
			w.CachedBytes = 0
			w.Files = 0
			w.BytesPerSecond = 0
			w.Path = ""
			w.FilePercent = nil
			m.lastBytes = 0
		}
		if e.Path != "" {
			w.Path = e.Path
		}
		if e.Source != "" {
			w.Source = e.Source
		}
		if e.Destination != "" {
			w.Destination = e.Destination
		}
		w.Bytes += e.Bytes
		if e.CacheHit {
			w.CachedFiles++
			w.CachedBytes += e.CacheBytes
		}
		if e.FileDone {
			w.Files++
		}
		if e.FilePercent != nil {
			v := *e.FilePercent
			w.FilePercent = &v
		}
		w.UpdatedAt = time.Now()
	}
}
func rates(old, cur diskCounter, dt float64) (*float64, *float64) {
	if dt <= 0 || cur.Read < old.Read || cur.Write < old.Write {
		return nil, nil
	}
	r := float64(cur.Read-old.Read) / dt
	w := float64(cur.Write-old.Write) / dt
	return &r, &w
}
func (s *Server) sampleMonitor(ctx context.Context) {
	counters, err := readDiskCounters(ctx, &s.monitor)
	now := time.Now()
	m := &s.monitor
	m.mu.Lock()
	defer m.mu.Unlock()
	sample := monitorSample{At: now, Disks: []diskMetric{}}
	if err != nil {
		m.error = err.Error()
	} else {
		m.error = ""
	}
	next := map[string]diskCounter{}
	dt := now.Sub(m.last).Seconds()
	for _, c := range counters {
		d := diskMetric{ID: c.ID, Name: c.Name, Device: c.Device, Location: c.Location}
		if old, ok := m.previous[c.ID]; ok && err == nil {
			d.ReadBPS, d.WriteBPS = rates(old, c, dt)
		}
		sample.Disks = append(sample.Disks, d)
		next[c.ID] = c
	}
	if dt > 0 && m.workPhase == m.work.Phase {
		m.work.BytesPerSecond = float64(m.work.Bytes-m.lastBytes) / dt
	}
	m.lastBytes = m.work.Bytes
	m.workPhase = m.work.Phase
	if m.work.Phase == "done" {
		m.work.BytesPerSecond = 0
	}
	sample.Work = m.work
	m.previous = next
	m.last = now
	m.history = append(m.history, sample)
	if len(m.history) > 120 {
		m.history = m.history[len(m.history)-120:]
	}
}
func (s *Server) startMonitor(ctx context.Context) {
	go func() {
		tick := time.NewTicker(time.Second)
		defer tick.Stop()
		for {
			s.sampleMonitor(ctx)
			select {
			case <-ctx.Done():
				return
			case <-tick.C:
			}
		}
	}()
}

// Small read-only XML plist decoder used for Apple's ioreg/diskutil output.
func plistValue(d *xml.Decoder, start xml.StartElement) (any, error) {
	switch start.Name.Local {
	case "dict":
		v := map[string]any{}
		key := ""
		for {
			t, e := d.Token()
			if e != nil {
				return nil, e
			}
			switch t := t.(type) {
			case xml.EndElement:
				return v, nil
			case xml.StartElement:
				if t.Name.Local == "key" {
					if e = d.DecodeElement(&key, &t); e != nil {
						return nil, e
					}
				} else {
					x, e := plistValue(d, t)
					if e != nil {
						return nil, e
					}
					v[key] = x
				}
			}
		}
	case "array":
		v := []any{}
		for {
			t, e := d.Token()
			if e != nil {
				return nil, e
			}
			switch t := t.(type) {
			case xml.EndElement:
				return v, nil
			case xml.StartElement:
				x, e := plistValue(d, t)
				if e != nil {
					return nil, e
				}
				v = append(v, x)
			}
		}
	default:
		var v string
		if e := d.DecodeElement(&v, &start); e != nil {
			return nil, e
		}
		if start.Name.Local == "integer" {
			n, _ := strconv.ParseUint(v, 10, 64)
			return n, nil
		}
		if start.Name.Local == "true" {
			return true, nil
		}
		if start.Name.Local == "false" {
			return false, nil
		}
		return v, nil
	}
}
func decodePlist(r io.Reader) (any, error) {
	d := xml.NewDecoder(r)
	for {
		t, e := d.Token()
		if e != nil {
			return nil, e
		}
		if st, ok := t.(xml.StartElement); ok && st.Name.Local != "plist" {
			return plistValue(d, st)
		}
	}
}
func readDiskCounters(ctx context.Context, m *monitorState) ([]diskCounter, error) {
	if runtime.GOOS != "darwin" {
		return nil, fmt.Errorf("当前平台不提供 macOS 设备读写计数器")
	}
	c, cancel := context.WithTimeout(ctx, 900*time.Millisecond)
	defer cancel()
	out, e := exec.CommandContext(c, "/usr/sbin/ioreg", "-r", "-c", "IOBlockStorageDriver", "-l", "-a", "-d", "2").Output()
	if e != nil {
		return nil, fmt.Errorf("磁盘采样不可用: %w", e)
	}
	value, e := decodePlistBytes(out)
	if e != nil {
		return nil, e
	}
	a, ok := value.([]any)
	if !ok {
		return nil, fmt.Errorf("设备计数器格式无效")
	}
	result := []diskCounter{}
	for _, v := range a {
		x, ok := v.(map[string]any)
		if !ok {
			continue
		}
		stats, _ := x["Statistics"].(map[string]any)
		read, rok := stats["Bytes (Read)"].(uint64)
		write, wok := stats["Bytes (Write)"].(uint64)
		if !rok || !wok {
			continue
		}
		children, _ := x["IORegistryEntryChildren"].([]any)
		for _, child := range children {
			media, ok := child.(map[string]any)
			if !ok {
				continue
			}
			device, _ := media["BSD Name"].(string)
			if device == "" || media["Whole"] != true {
				continue
			}
			id := fmt.Sprint(x["IORegistryEntryID"])
			info := diskCounter{ID: id, Device: device, Name: fmt.Sprint(media["IORegistryEntryName"]), Location: "unknown"}
			if m.diskInfo == nil {
				m.diskInfo = map[string]diskCounter{}
			}
			if cached, ok := m.diskInfo[id]; ok {
				info = cached
			} else if time.Now().After(m.diskInfoRetry[id]) {
				if m.diskInfoRetry == nil {
					m.diskInfoRetry = map[string]time.Time{}
				}
				// A failed or sleeping device must not spawn diskutil every second.
				m.diskInfoRetry[id] = time.Now().Add(time.Minute)
				b, er := exec.CommandContext(c, "/usr/sbin/diskutil", "info", "-plist", device).Output()
				if er == nil {
					p, er := decodePlistBytes(b)
					if er == nil {
						if d, ok := p.(map[string]any); ok {
							if internal, ok := d["Internal"].(bool); ok {
								if internal {
									info.Location = "internal"
								} else {
									info.Location = "external"
								}
							}
							m.diskInfo[id] = info
						}
					}
				}
			}
			info.Read = read
			info.Write = write
			result = append(result, info)
		}
	}
	return result, nil
}

func decodePlistBytes(b []byte) (any, error) { return decodePlist(bytes.NewReader(b)) }
