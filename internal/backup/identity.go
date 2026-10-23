package backup

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"syscall"
)

func populateIdentity(en *Entry, st os.FileInfo, volume string) {
	en.VolumeID = volume
	if a, ok := st.Sys().(*syscall.Stat_t); ok {
		en.FileID = strconv.FormatUint(a.Ino, 10)
		en.Identity = fmt.Sprintf("%d:%d", a.Dev, a.Ino)
		en.ChangeTime, en.BirthTime = statTimes(a)
	}
}
func samePhysicalVersion(a, b os.FileInfo) bool {
	x, y := Entry{}, Entry{}
	populateIdentity(&x, a, "")
	populateIdentity(&y, b, "")
	return a.Mode() == b.Mode() && a.Size() == b.Size() && a.ModTime().UnixNano() == b.ModTime().UnixNano() && x.Identity == y.Identity && x.ChangeTime == y.ChangeTime && x.BirthTime == y.BirthTime
}

// ContentKey identifies a version of a physical object, never merely its inode.
func ContentKey(en Entry) string {
	if en.Kind != "file" || en.VolumeID == "" || en.FileID == "" || en.ChangeTime == 0 {
		return ""
	}
	b, _ := json.Marshal(struct {
		Algorithm, Volume, File   string
		Size, Mtime, Ctime, Birth int64
		Mode                      uint32
		Attributes                string
	}{"sha256-v1", en.VolumeID, en.FileID, en.Size, en.ModTime, en.ChangeTime, en.BirthTime, en.Mode, en.AttributeHash})
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

func identityDevice(st os.FileInfo) string {
	if a, ok := st.Sys().(*syscall.Stat_t); ok {
		return fmt.Sprint(a.Dev)
	}
	return ""
}

// MetadataVersion augments a cheap polling signature. It is a change hint;
// only complete content verification is publication authority.
func MetadataVersion(st os.FileInfo) string {
	en := Entry{}
	populateIdentity(&en, st, "")
	return fmt.Sprintf("%s:%d:%d", en.Identity, en.ChangeTime, en.BirthTime)
}
