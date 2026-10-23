package backup

import (
	"context"
	"errors"
	"time"
)

type IndexedPath struct {
	Cacheable     bool
	Hashed        bool
	Entry         Entry
	State, Reason string
}
type SourceScope struct {
	Partial                                                    bool
	BaseGeneration                                             int64
	WalkedPaths                                                int
	FullScanAt                                                 time.Time
	Generation                                                 int64
	SourceID, Root, RootIdentity, VolumeID, Filesystem, Policy string
	Cacheable                                                  bool
	ScannedAt                                                  time.Time
}
type FingerprintIndex interface {
	BeginSource(context.Context, string) (int64, error)
	SourceHashes(context.Context, string) (map[string]string, error)
	LookupHash(context.Context, string) (string, error)
	CommitSource(context.Context, SourceScope, []IndexedPath) error
}
type SourceProjection struct {
	Scope SourceScope
	Paths []IndexedPath
}
type ProjectionIndex interface {
	LoadSourceProjection(context.Context, string) (*SourceProjection, error)
}

var ErrIndexStale = errors.New("目录索引已被较新扫描替换")

type ScanStats struct {
	Timings        ScanTimings `json:"timings"`
	FullSources    int         `json:"fullSources"`
	SubtreeSources int         `json:"subtreeSources"`
	PathsVisited   int         `json:"pathsVisited"`
	ProjectedPaths int         `json:"projectedPaths"`
	FilesHashed    int         `json:"filesHashed"`
	CacheHits      int         `json:"cacheHits"`
	BytesHashed    int64       `json:"bytesHashed"`
	BytesReused    int64       `json:"bytesReused"`
	Forced         bool        `json:"forced"`
}

// Directory, selection, attribute/stat, fingerprint and validation timings are
// subsets of WalkMS, not additional phases.
type ScanTimings struct {
	SelectionMS        float64 `json:"selectionMs"`
	FingerprintMS      float64 `json:"fingerprintMs"`
	SourceValidationMS float64 `json:"sourceValidationMs"`
	DirectoryReadMS    float64 `json:"directoryReadMs"`
	IdentityMS         float64 `json:"identityMs"`
	IndexReadMS        float64 `json:"indexReadMs"`
	WalkMS             float64 `json:"walkMs"`
	AttributesMS       float64 `json:"attributesMs"`
	InitialStatMS      float64 `json:"initialStatMs"`
	FinalStatMS        float64 `json:"finalStatMs"`
	IndexCommitMS      float64 `json:"indexCommitMs"`
	SortMS             float64 `json:"sortMs"`
}
type ScanResult struct {
	Entries []Entry
	Ignored []Operation
	Stats   ScanStats
}

func validHash(hash string) bool {
	if len(hash) != 64 {
		return false
	}
	for _, c := range hash {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

// Rule tests may reuse proven physical fingerprints but must not replace the
// saved source's policy/generation with observations from an unsaved draft.
type readOnlyIndex struct{ FingerprintIndex }

func ReadOnlyIndex(index FingerprintIndex) FingerprintIndex                          { return readOnlyIndex{index} }
func (readOnlyIndex) BeginSource(context.Context, string) (int64, error)             { return 0, nil }
func (readOnlyIndex) CommitSource(context.Context, SourceScope, []IndexedPath) error { return nil }
