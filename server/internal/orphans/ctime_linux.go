//go:build linux

package orphans

import (
	"fmt"
	"io/fs"
	"syscall"
	"time"
)

// ctime returns the inode status-change time for info. On Linux this is
// read from the st_ctim field of the syscall.Stat_t. Unlike mtime/atime,
// ctime cannot be set by os.Chtimes, so it is the one timestamp the orphan
// scan's age guard can trust even though filestore.MoveFile now writes
// client-controlled mtime/atime values (see docs/adr/0006).
func ctime(info fs.FileInfo) (time.Time, error) {
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return time.Time{}, fmt.Errorf("orphans.ctime: unexpected sys stat type %T", info.Sys())
	}
	return time.Unix(st.Ctim.Sec, st.Ctim.Nsec), nil
}
