package main

import (
	"context"
	"fmt"
	"io/fs"
	"path/filepath"
)

type imageStorage struct {
	allocated int64
	logical   int64
}

func imageSize(ctx context.Context, directory string) (imageStorage, error) {
	var size imageStorage

	err := filepath.WalkDir(directory, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		err = ctx.Err()
		if err != nil {
			return err
		}

		if !entry.Type().IsRegular() {
			return nil
		}

		info, err := entry.Info()
		if err != nil {
			return err
		}

		allocated, err := allocatedFileSize(path, info)
		if err != nil {
			return fmt.Errorf("allocated size of %q: %w", path, err)
		}

		size.allocated += allocated
		size.logical += info.Size()

		return nil
	})

	return size, err
}

func formatImageSize(size int64) string {
	if size < 1024 {
		return fmt.Sprintf("%d B", size)
	}

	units := [...]string{"KiB", "MiB", "GiB", "TiB", "PiB", "EiB"}
	amount := float64(size) / 1024
	unit := 0

	for amount >= 1024 && unit < len(units)-1 {
		amount /= 1024
		unit++
	}

	return fmt.Sprintf("%.2f %s", amount, units[unit])
}
