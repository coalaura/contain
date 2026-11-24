//go:build !windows

package create

import "os"

func sizeDisk(disk *os.File, size int64) error {
	return disk.Truncate(size)
}
