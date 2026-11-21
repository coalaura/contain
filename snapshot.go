package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
)

type GuestSnapshot struct {
	HostPath  string
	GuestPath string
}

func (c *Config) GuestSnapshots(mounts []GuestMount) ([]GuestSnapshot, error) {
	snapshots := make([]GuestSnapshot, 0, len(c.Snapshots))

	for index := range c.Snapshots {
		snapshot := &c.Snapshots[index]

		target, err := resolveGuestPath(snapshot.Guest)
		if err != nil {
			return nil, fmt.Errorf("snapshots[%d].guest: %w", index, err)
		}

		for previous := range snapshots {
			other := snapshots[previous].GuestPath
			if guestPathsOverlap(target, other) {
				return nil, fmt.Errorf("snapshots[%d].guest overlaps snapshot destination %q", index, other)
			}
		}

		for mountIndex := range mounts {
			mounted := mounts[mountIndex].GuestPath
			if guestPathsOverlap(target, mounted) {
				return nil, fmt.Errorf("snapshots[%d].guest overlaps host mount %q", index, mounted)
			}
		}

		host, err := resolveHostPath(snapshot.Host)
		if err != nil {
			return nil, fmt.Errorf("snapshots[%d].host: %w", index, err)
		}

		info, err := os.Stat(host)
		if err != nil {
			return nil, fmt.Errorf("snapshots[%d].host: %w", index, err)
		}

		if !info.Mode().IsRegular() {
			return nil, fmt.Errorf("snapshots[%d].host must be a regular file", index)
		}

		snapshots = append(snapshots, GuestSnapshot{HostPath: host, GuestPath: target})
	}

	return snapshots, nil
}

func prepareGuestSnapshots(snapshots []GuestSnapshot) (string, func(), error) {
	if len(snapshots) == 0 {
		return "", func() {}, nil
	}

	directory, err := os.MkdirTemp(paths.DataDirectory, "contain-snapshots-*")
	if err != nil {
		return "", nil, fmt.Errorf("create snapshot directory: %w", err)
	}

	cleanup := func() {
		os.RemoveAll(directory)
	}

	for index := range snapshots {
		destination := filepath.Join(directory, strconv.Itoa(index))

		err = copySnapshot(snapshots[index].HostPath, destination)
		if err != nil {
			cleanup()

			return "", nil, fmt.Errorf("snapshot %q: %w", snapshots[index].HostPath, err)
		}
	}

	return directory, cleanup, nil
}

func copySnapshot(source, destination string) error {
	input, err := os.Open(source)
	if err != nil {
		return err
	}

	defer input.Close()

	info, err := input.Stat()
	if err != nil {
		return err
	}

	if !info.Mode().IsRegular() {
		return errors.New("source must be a regular file")
	}

	output, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}

	_, copyErr := io.Copy(output, input)
	closeErr := output.Close()

	return errors.Join(copyErr, closeErr)
}

func guestPathsOverlap(first, second string) bool {
	return first == second || guestPathWithin(first, second) || guestPathWithin(second, first)
}
