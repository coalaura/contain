//go:build !windows

package main

import (
	"errors"
	"io/fs"
	"syscall"
)

func allocatedFileSize(_ string, info fs.FileInfo) (int64, error) {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, errors.New("file allocation metadata unavailable")
	}

	// st_blocks counts allocated 512-byte units, independently of the filesystem block size.
	return stat.Blocks * 512, nil
}
