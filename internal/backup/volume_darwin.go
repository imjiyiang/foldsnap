package backup

import "syscall"

func mountPoint(path string) (string, error) {
	var st syscall.Statfs_t
	if e := syscall.Statfs(path, &st); e != nil {
		return "", e
	}
	b := make([]byte, 0, len(st.Mntonname))
	for _, c := range st.Mntonname {
		if c == 0 {
			break
		}
		b = append(b, byte(c))
	}
	return string(b), nil
}
