package backup

/*
#include <sys/attr.h>
#include <unistd.h>
#include <sys/param.h>
#include <uuid/uuid.h>
#include <stdlib.h>
#include <string.h>
#include <errno.h>

static int lb_fullpath(const char *path, char *out) {
 struct attrlist attrs = {0};
 attrs.bitmapcount = ATTR_BIT_MAP_COUNT;
 attrs.commonattr = ATTR_CMN_FULLPATH;
 struct { uint32_t length; attrreference_t ref; char bytes[PATH_MAX]; } buf = {0};
 if (getattrlist(path, &attrs, &buf, sizeof(buf), 0) != 0) return errno;
 long offset = (long)((char *)&buf.ref - (char *)&buf) + buf.ref.attr_dataoffset;
 size_t len = buf.ref.attr_length;
 if (offset < 0 || len == 0 || len > PATH_MAX || offset + len > buf.length || buf.length > sizeof(buf)) return EINVAL;
 const char *value = (const char *)&buf + offset;
 if (value[len-1] != 0) return EINVAL;
 memcpy(out, value, len);
 return 0;
}
static int lb_volume_uuid(const char *path, char *out) {
 struct attrlist attrs = {0};
 attrs.bitmapcount = ATTR_BIT_MAP_COUNT;
 attrs.volattr = ATTR_VOL_INFO | ATTR_VOL_UUID;
 struct { uint32_t length; uuid_t uuid; } buf = {0};
 if (getattrlist(path, &attrs, &buf, sizeof(buf), 0) != 0) return errno;
 if (buf.length != sizeof(buf) || uuid_is_null(buf.uuid)) return EINVAL;
 uuid_unparse_upper(buf.uuid, out);
 return 0;
}
*/
import "C"

import (
	"syscall"
	"unsafe"
)

// Query filesystem metadata without opening the directory for reading. In a
// GUI-launched process, a read-open can wait for macOS access mediation.
func canonicalPlatform(path string) (string, error) {
	p := C.CString(path)
	defer C.free(unsafe.Pointer(p))
	var out [C.PATH_MAX]C.char
	if code := C.lb_fullpath(p, &out[0]); code != 0 {
		return "", syscall.Errno(code)
	}
	return C.GoString(&out[0]), nil
}

// The filesystem UUID is the same identity diskutil reports, obtained without
// diskutil probing raw devices or spawning a process for each directory check.
func VolumeID(path string) (string, error) {
	mount, err := mountPoint(path)
	if err != nil {
		return "", err
	}
	p := C.CString(mount)
	defer C.free(unsafe.Pointer(p))
	var out [37]C.char
	if code := C.lb_volume_uuid(p, &out[0]); code != 0 {
		return "", syscall.Errno(code)
	}
	return C.GoString(&out[0]), nil
}
