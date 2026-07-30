//go:build darwin

package localfs

import (
	"time"

	"golang.org/x/sys/unix"
)

func statModTime(stat unix.Stat_t) time.Time {
	return time.Unix(stat.Mtim.Sec, stat.Mtim.Nsec)
}
