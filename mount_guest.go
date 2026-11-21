package main

import (
	"context"
	_ "embed"
	"fmt"
	"strings"
)

//go:embed mount_guest.sh
var guestMountScript string

// Script tabs are indentation; omit them so console input cannot trigger completion.
var guestMountCommandPrefix = "sh -c " + shellQuote(strings.ReplaceAll(guestMountScript, "\t", "")) + " contain"

func mountGuestFilesystems(ctx context.Context, con *console, mounts []GuestMount) error {
	err := con.runShell(ctx, guestMountCommand(mounts))
	if err != nil {
		return fmt.Errorf("prepare guest mounts: %w", err)
	}

	return nil
}

func guestMountCommand(mounts []GuestMount) string {
	size := len(guestMountCommandPrefix)

	for index := range mounts {
		mount := &mounts[index]
		size += len(mount.GuestPath) + len(mount.ParentPath) + 3*(strings.Count(mount.GuestPath, "'")+strings.Count(mount.ParentPath, "'")) + len(mount.Tag) + 14
	}

	var command strings.Builder

	command.Grow(size)
	command.WriteString(guestMountCommandPrefix)

	// Children precede their parents for rollback and shutdown cleanup.
	for index := len(mounts) - 1; index >= 0; index-- {
		mount := &mounts[index]

		// Keep each mount on its own input line to stay within terminal line limits.
		command.WriteString(" \\\n")
		command.WriteString(shellQuote(mount.GuestPath))
		command.WriteByte(' ')
		command.WriteString(shellQuote(mount.Tag))

		if mount.Readonly {
			command.WriteString(" ro")
		} else {
			command.WriteString(" rw")
		}

		command.WriteByte(' ')
		command.WriteString(shellQuote(mount.ParentPath))
	}

	return command.String()
}
