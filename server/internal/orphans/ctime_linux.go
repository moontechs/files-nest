//go:build linux

package orphans

import (
	"errors"
	"fmt"
	"io/fs"
	"syscall"
	"time"
)

// errUnexpectedStatType is returned by ctime when the platform's
// syscall.Stat_t type assertion fails.
var errUnexpectedStatType = errors.New("orphans.ctime: unexpected sys stat type")

// ctime returns the inode status-change time for info. On Linux this is
// read from the st_ctim field of the syscall.Stat_t. Unlike mtime/atime,
// ctime cannot be set by os.Chtimes, so it is the one timestamp the orphan
// scan's age guard can trust even though filestore.MoveFile now writes
// client-controlled mtime/atime values (see docs/adr/0006).
func ctime(info fs.FileInfo) (time.Time, error) {
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return time.Time{}, fmt.Errorf("%w: %T", errUnexpectedStatType, info.Sys())
	}

	return time.Unix(st.Ctim.Sec, st.Ctim.Nsec), nil
}
