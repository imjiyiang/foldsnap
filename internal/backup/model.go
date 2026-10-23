package backup

import (
	"crypto/rand"
	"encoding/hex"
	"time"
)

type Rule struct {
	Action  string `json:"action"`
	Pattern string `json:"pattern"`
}
type Source struct {
	VolumeID           string       `json:"volumeId,omitempty"`
	DirectoryID        string       `json:"directoryId,omitempty"`
	DirectoryBirthTime string       `json:"directoryBirthTime,omitempty"`
	MountPath          string       `json:"mountPath,omitempty"`
	VolumeRelativePath string       `json:"volumeRelativePath,omitempty"`
	FileFilters        *FileFilters `json:"fileFilters,omitempty"`
	ID                 string       `json:"id"`
	Name               string       `json:"name"`
	Path               string       `json:"path"`
	TargetPath         string       `json:"targetPath"`
	Enabled            bool         `json:"enabled"`
	ExcludeHidden      bool         `json:"excludeHidden"`
	Rules              []Rule       `json:"rules"`
}
type Destination struct {
	MountPath          string `json:"mountPath,omitempty"`
	VolumeRelativePath string `json:"volumeRelativePath,omitempty"`
	ID                 string `json:"id"`
	Name               string `json:"name"`
	Path               string `json:"path"`
	VolumeID           string `json:"volumeId"`
	Enabled            bool   `json:"enabled"`
}
type Schedule struct {
	MinChangedFiles    int   `json:"minChangedFiles,omitempty"`
	MinChangedBytes    int64 `json:"minChangedBytes,omitempty"`
	MinIntervalMinutes *int  `json:"minIntervalMinutes,omitempty"`
	MaxIntervalMinutes int   `json:"maxIntervalMinutes,omitempty"`
	MaxAttempts        *int  `json:"maxAttempts,omitempty"`
	IntervalMinutes    int   `json:"intervalMinutes"`
	OnMount            bool  `json:"onMount"`
	OnChange           bool  `json:"onChange"`
	DebounceSeconds    int   `json:"debounceSeconds"`
}
type Retention struct {
	KeepLast    int `json:"keepLast"`
	KeepDays    int `json:"keepDays"`
	KeepDaily   int `json:"keepDaily,omitempty"`
	KeepWeekly  int `json:"keepWeekly,omitempty"`
	KeepMonthly int `json:"keepMonthly,omitempty"`
}
type Plan struct {
	RecoveryPlan            *Plan               `json:"-"`
	RuntimeDirtyRoots       map[string][]string `json:"-"`
	ExecutionSourceIDs      []string            `json:"-"`
	ExpectedViewDigests     map[string]string   `json:"-"`
	EvaluatedAt             time.Time           `json:"-"`
	FileFilters             FileFilters         `json:"fileFilters"`
	ExpectedBaselineDigests map[string]string   `json:"-"`
	MirrorDeletesAuthorized bool                `json:"-"`
	ExpectedCurrentIDs      map[string]string   `json:"-"`
	ExpectedBaselines       map[string]string   `json:"-"`
	RuntimeExcludes         []string            `json:"-"`
	ID                      string              `json:"id"`
	Name                    string              `json:"name"`
	Description             string              `json:"description"`
	Mode                    string              `json:"mode"`
	Enabled                 bool                `json:"enabled"`
	Sources                 []Source            `json:"sources"`
	Destinations            []Destination       `json:"destinations"`
	Rules                   []Rule              `json:"rules"`
	Schedule                Schedule            `json:"schedule"`
	Retention               Retention           `json:"retention"`
	CreatedAt               time.Time           `json:"createdAt"`
	UpdatedAt               time.Time           `json:"updatedAt"`
}
type Entry struct {
	VolumeID      string `json:"volumeId,omitempty"`
	FileID        string `json:"fileId,omitempty"`
	ChangeTime    int64  `json:"ctime,omitempty"`
	BirthTime     int64  `json:"birthtime,omitempty"`
	BasicMetadata bool   `json:"basicMetadata,omitempty"`
	AttributeHash string `json:"attributeHash,omitempty"`
	SourceID      string `json:"sourceId"`
	Path          string `json:"path"`
	Kind          string `json:"kind"`
	Size          int64  `json:"size"`
	ModTime       int64  `json:"mtime"`
	Mode          uint32 `json:"mode"`
	Hash          string `json:"hash,omitempty"`
	Link          string `json:"link,omitempty"`
	Identity      string `json:"identity,omitempty"`
}
type Operation struct {
	SourceID      string `json:"sourceId"`
	DestinationID string `json:"destinationId"`
	Path          string `json:"path"`
	OldPath       string `json:"oldPath,omitempty"`
	Type          string `json:"type"`
	Size          int64  `json:"size"`
	Reason        string `json:"reason,omitempty"`
}
type Preview struct {
	PreflightCacheHits int               `json:"preflightCacheHits,omitempty"`
	SourceIDs          []string          `json:"sourceIds,omitempty"`
	ScanStats          *ScanStats        `json:"scanStats,omitempty"`
	ViewDigests        map[string]string `json:"-"`
	EvaluatedAt        time.Time         `json:"evaluatedAt"`
	BaselineDigests    map[string]string `json:"-"`
	Removals           []Operation       `json:"removals"`
	PreviewID          string            `json:"previewId,omitempty"`
	ExpiresAt          *time.Time        `json:"expiresAt,omitempty"`
	CurrentIDs         map[string]string `json:"-"`
	BaselineIDs        map[string]string `json:"-"`
	Fingerprint        string            `json:"-"`
	DeletePaths        int               `json:"deletePaths"`
	DeleteBytes        int64             `json:"deleteBytes"`
	Retained           int               `json:"retained"`
	RetainedBytes      int64             `json:"retainedBytes"`
	Operations         []Operation       `json:"operations"`
	Files              int               `json:"files"`
	Bytes              int64             `json:"bytes"`
	TransferBytes      int64             `json:"transferBytes"`
	Added              int               `json:"added"`
	Modified           int               `json:"modified"`
	Moved              int               `json:"moved"`
	Deleted            int               `json:"deleted"`
	Reused             int               `json:"reused"`
	Warnings           []string          `json:"warnings"`
}
type Run struct {
	NoChanges               bool       `json:"noChanges,omitempty"`
	CreatedSnapshotIDs      []string   `json:"createdSnapshotIds,omitempty"`
	UnchangedDestinationIDs []string   `json:"unchangedDestinationIds,omitempty"`
	AutomaticAttempt        int        `json:"automaticAttempt,omitempty"`
	RetryOf                 string     `json:"retryOf,omitempty"`
	SourceIDs               []string   `json:"sourceIds,omitempty"`
	SourceNames             []string   `json:"sourceNames,omitempty"`
	DestinationIDs          []string   `json:"destinationIds,omitempty"`
	AttemptID               string     `json:"attemptId,omitempty"`
	QueuedAt                *time.Time `json:"queuedAt,omitempty"`
	ApprovedPreviewID       string     `json:"approvedPreviewId,omitempty"`
	DeletesConfirmed        bool       `json:"deletesConfirmed"`
	ID                      string     `json:"id"`
	PlanID                  string     `json:"planId"`
	PlanName                string     `json:"planName"`
	Status                  string     `json:"status"`
	Stage                   string     `json:"stage"`
	Progress                int        `json:"progress"`
	StartedAt               time.Time  `json:"startedAt"`
	FinishedAt              *time.Time `json:"finishedAt,omitempty"`
	Error                   string     `json:"error,omitempty"`
	Logs                    []string   `json:"logs"`
	Preview                 *Preview   `json:"preview,omitempty"`
	Trigger                 string     `json:"trigger"`
}
type Snapshot struct {
	PlanConfig         *Plan     `json:"planConfig,omitempty"`
	UpdatedSourceIDs   []string  `json:"updatedSourceIds,omitempty"`
	Mode               string    `json:"mode,omitempty"`
	CurrentPath        string    `json:"currentPath,omitempty"`
	MountPath          string    `json:"mountPath,omitempty"`
	VolumeRelativePath string    `json:"volumeRelativePath,omitempty"`
	ExtendedMetadata   bool      `json:"extendedMetadata"`
	ID                 string    `json:"id"`
	PlanID             string    `json:"planId"`
	PlanName           string    `json:"planName"`
	DestinationID      string    `json:"destinationId"`
	DestinationName    string    `json:"destinationName"`
	DestinationPath    string    `json:"destinationPath"`
	VolumeID           string    `json:"volumeId"`
	RepositoryID       string    `json:"repositoryId"`
	Path               string    `json:"path"`
	CreatedAt          time.Time `json:"createdAt"`
	Files              int       `json:"files"`
	Bytes              int64     `json:"bytes"`
	Pinned             bool      `json:"pinned"`
	Entries            []Entry   `json:"entries,omitempty"`
	Sources            []Source  `json:"sources,omitempty"`
}
type Settings struct {
	RsyncPath   string `json:"rsyncPath"`
	PollSeconds int    `json:"pollSeconds"`
}

func ID(prefix string) string {
	b := make([]byte, 12)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return prefix + hex.EncodeToString(b)
}
