package main

import (
	"context"
	_ "embed"
	"fmt"
	"strings"
)

//go:embed snapshot_guest.sh
var guestSnapshotScript string

// Omit indentation tabs to avoid triggering completion in the login shell.
var guestSnapshotCommandPrefix = "bash -c " + shellQuote(strings.ReplaceAll(guestSnapshotScript, "\t", "")) + " contain"

func copyGuestSnapshots(ctx context.Context, con *console, snapshots []GuestSnapshot) error {
	if len(snapshots) == 0 {
		return nil
	}

	err := con.runShell(ctx, guestSnapshotCommand(snapshots))
	if err != nil {
		return fmt.Errorf("copy guest snapshots: %w", err)
	}

	return nil
}

func guestSnapshotCommand(snapshots []GuestSnapshot) string {
	size := len(guestSnapshotCommandPrefix)

	for index := range snapshots {
		target := snapshots[index].GuestPath
		size += len(target) + 3*strings.Count(target, "'") + 6
	}

	var command strings.Builder

	command.Grow(size)

	command.WriteString(guestSnapshotCommandPrefix)

	for index := range snapshots {
		command.WriteString(" \\\n")
		command.WriteString(shellQuote(snapshots[index].GuestPath))
	}

	return command.String()
}
