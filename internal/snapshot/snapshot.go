package snapshot

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"

	"github.com/coalaura/contain/internal/mount"
	"github.com/coalaura/contain/internal/pathutil"
)

type Config struct {
	Host  string `yaml:"host"`
	Guest string `yaml:"guest"`
}

type Snapshot struct {
	HostPath  string
	GuestPath string
}

func Resolve(configured []Config, mounts []mount.Mount) ([]Snapshot, error) {
	snapshots := make([]Snapshot, 0, len(configured))

	for index := range configured {
		snapshot := &configured[index]

		target, err := pathutil.Guest(snapshot.Guest)
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

		host, err := pathutil.Host(snapshot.Host)
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

		snapshots = append(snapshots, Snapshot{HostPath: host, GuestPath: target})
	}

	return snapshots, nil
}

func Prepare(imageDirectory string, snapshots []Snapshot) (string, func(), error) {
	if len(snapshots) == 0 {
		return "", func() {}, nil
	}

	directory, err := os.MkdirTemp(imageDirectory, "contain-snapshots-*")
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
	return first == second || pathutil.GuestWithin(first, second) || pathutil.GuestWithin(second, first)
}
