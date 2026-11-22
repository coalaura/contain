package main

import (
	"github.com/coalaura/contain/internal/mount"
	"github.com/coalaura/contain/internal/network"
	"github.com/coalaura/contain/internal/snapshot"
)

func validateNetworkShares(mounts []mount.Mount, snapshots []snapshot.Snapshot) error {
	directories := make([]string, len(mounts))
	files := make([]string, len(snapshots))

	for index := range mounts {
		directories[index] = mounts[index].HostPath
	}

	for index := range snapshots {
		files[index] = snapshots[index].HostPath
	}

	return network.ValidateShares(paths.DataDirectory, config.Machine.directory, directories, files)
}
