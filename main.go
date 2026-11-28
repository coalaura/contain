package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
	"text/tabwriter"

	imagecreate "github.com/coalaura/contain/internal/create"
	"github.com/coalaura/contain/internal/mount"
	"github.com/coalaura/contain/internal/network"
	"github.com/coalaura/contain/internal/snapshot"
	"github.com/coalaura/plain/minimal"
	"github.com/urfave/cli/v3"
)

var (
	paths  *Paths
	config *Config

	version = "dev"
	log     = minimal.New(minimal.ToStdErr())
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	defer stop()

	mountFlag := &cli.StringSliceFlag{
		Name:    "mount",
		Aliases: []string{"m"},
		Usage:   "Add a session mount: HOST[:NAME][:ro|rw] (repeatable; name defaults to the directory basename)",
	}

	mountFlag.SetStringer(func(cli.Flag) string {
		return cli.FlagNamePrefixer(mountFlag.Names(), "SPEC") + "\t" + mountFlag.Usage
	})

	cmd := &cli.Command{
		Name:            "contain",
		Usage:           "Run tools and agents in an isolated Linux workspace",
		UsageText:       "contain [run] [options]\ncontain create [--image NAME] [--size 8G] [--kernel virt|lts|stable] [--tools]\ncontain list\ncontain remove NAME",
		Version:         version,
		HideHelpCommand: true,
		DefaultCommand:  "run",
		Commands: []*cli.Command{
			{
				Name:                      "run",
				Usage:                     "Run tools and agents in an isolated Linux workspace (default)",
				UsageText:                 "contain [run] [options]",
				DisableSliceFlagSeparator: true,
				Flags: []cli.Flag{
					imageFlag(),
					&cli.BoolFlag{
						Name:    "plain",
						Aliases: []string{"p"},
						Usage:   "Start a shell at / without mounts, snapshots or a startup command",
					},
					&cli.BoolFlag{
						Name:    "shell",
						Aliases: []string{"s"},
						Usage:   "Start a shell instead of the configured command",
					},
					&cli.StringFlag{
						Name:    "run",
						Aliases: []string{"r"},
						Usage:   "Run `command` instead of the configured command",
					},
					mountFlag,
					&cli.BoolFlag{
						Name:  "readonly",
						Usage: "Mount the workspace read-only",
					},
					&cli.StringSliceFlag{
						Name:    "publish",
						Aliases: []string{"forward"},
						Usage:   "Publish a guest port: [HOST_IP:]HOST:GUEST[/tcp|udp] (repeatable; host defaults to 127.0.0.1)",
					},
					&cli.BoolFlag{
						Name:    "temporary",
						Aliases: []string{"t", "temp"},
						Usage:   "Discard guest disk changes on exit using a memory-backed layer",
					},
					&cli.BoolFlag{
						Name:    "dry-run",
						Aliases: []string{"d", "dry"},
						Usage:   "Preview mounts and hidden guest files, snapshots, ports, disk mode and command without starting the VM",
					},
				},
				Action: run,
			},
			{
				Name:      "create",
				Usage:     "Create an Alpine Linux guest image",
				UsageText: "contain create [--image NAME] [--size 8G] [--kernel virt|lts|stable] [--tools]",
				Flags: []cli.Flag{
					imageFlag(),
					&cli.StringFlag{
						Name:    "size",
						Aliases: []string{"s"},
						Usage:   "Disk size in bytes or with a K, M, G or T suffix (binary units; minimum 512M)",
						Value:   "8G",
					},
					&cli.StringFlag{
						Name:  "kernel",
						Usage: "Alpine kernel variation: virt (VM-optimized), lts (general-purpose), or stable (newer kernel from edge)",
						Value: imagecreate.DefaultKernel,
					},
					&cli.BoolFlag{
						Name:    "tools",
						Aliases: []string{"t"},
						Usage:   "Install common development tools and dependencies in the image",
					},
				},
				Action: create,
			},
			{
				Name:      "list",
				Usage:     "List images and their size",
				UsageText: "contain list",
				Action:    listImages,
			},
			{
				Name:      "remove",
				Usage:     "Permanently delete an image after confirming its name",
				UsageText: "contain remove NAME",
				Action:    removeImage,
			},
		},
	}

	err := cmd.Run(ctx, os.Args)
	if errors.Is(err, context.Canceled) {
		log.Infoln("Session canceled.")

		os.Exit(1)
	}

	guestExit, ok := errors.AsType[guestExitError](err)
	if ok {
		os.Exit(int(guestExit))
	}

	log.MustExit(err)
}

func run(ctx context.Context, command *cli.Command) error {
	if command.Args().Present() {
		return fmt.Errorf("unexpected argument %q", command.Args().First())
	}

	plain := command.Bool("plain")
	if !plain && command.Bool("shell") && command.IsSet("run") {
		return errors.New("--shell and --run cannot be used together")
	}

	image := command.String("image")

	err := validateImageName(image)
	if err != nil {
		return err
	}

	log.Stepln("Resolving paths...")

	paths, err = ResolvePaths()
	if err != nil {
		return err
	}

	log.Stepln("Loading configuration...")

	config, err = LoadConfig(image)
	if err != nil {
		return err
	}

	config.Machine.Network.Ports = append(config.Machine.Network.Ports, command.StringSlice("publish")...)

	ports, err := network.ResolvePorts(config.Machine.Network.Ports)
	if err != nil {
		return err
	}

	err = config.Machine.Network.Validate()
	if err != nil {
		return err
	}

	var (
		mounts         []mount.Mount
		snapshots      []snapshot.Snapshot
		startupCommand string
	)

	if !plain {
		err = validateShellCommand(config.Machine.Command)
		if err != nil {
			return fmt.Errorf("machine.command %w", err)
		}

		mountValues := command.StringSlice("mount")
		additionalMounts := make([]mount.Config, len(mountValues))

		for index, value := range mountValues {
			additionalMounts[index], err = mount.Parse(value)
			if err != nil {
				return fmt.Errorf("--mount %q: %w", value, err)
			}
		}

		mounts, err = mount.Resolve(config.Mounts, paths.WorkingDirectory, additionalMounts...)
		if err != nil {
			return err
		}

		mounts[0].Readonly = command.Bool("readonly")

		snapshots, err = snapshot.Resolve(config.Snapshots, mounts)
		if err != nil {
			return err
		}

		startupCommand = config.Machine.Command

		if command.IsSet("run") {
			startupCommand = command.String("run")
		} else if command.Bool("shell") {
			startupCommand = ""
		}

		err = validateShellCommand(startupCommand)
		if err != nil {
			return fmt.Errorf("startup command %w", err)
		}
	}

	temporary := command.Bool("temporary")

	err = validateNetworkShares(mounts, snapshots)
	if err != nil {
		return err
	}

	if command.Bool("dry-run") {
		return printDryRun(command.Writer, config.Machine.disk, mounts, snapshots, ports, startupCommand, temporary)
	}

	err = RunOpenVMM(ctx, mounts, snapshots, startupCommand, temporary)
	if err != nil {
		return err
	}

	log.Writeln("")
	log.Infoln("Session ended.")

	return nil
}

func printDryRun(output io.Writer, diskPath string, mounts []mount.Mount, snapshots []snapshot.Snapshot, ports []network.Port, command string, temporary bool) error {
	inspections := mount.InspectTargets(diskPath, mounts)
	writer := tabwriter.NewWriter(output, 0, 4, 2, ' ', 0)

	_, err := fmt.Fprintln(writer, "Mounts:\n  GUEST\tACCESS\tOVERRIDES FILES\tHOST")
	if err != nil {
		return err
	}

	if len(mounts) == 0 {
		_, err = fmt.Fprintln(writer, "  (none)")
		if err != nil {
			return err
		}
	}

	for index := range mounts {
		mount := &mounts[index]
		access := "read/write"

		if mount.Readonly {
			access = "read-only"
		}

		_, err = fmt.Fprintf(writer, "  %s\t%s\t%s\t%s\n", mount.GuestPath, access, inspections[index].Status, mount.HostPath)
		if err != nil {
			return err
		}
	}

	err = writer.Flush()
	if err != nil {
		return err
	}

	for index := range inspections {
		inspection := &inspections[index]
		if inspection.Err == nil {
			continue
		}

		_, err = fmt.Fprintf(output, "  Cannot inspect %s: %v\n", mounts[index].GuestPath, inspection.Err)
		if err != nil {
			return err
		}
	}

	if len(snapshots) > 0 {
		_, err = fmt.Fprintln(writer, "\nSnapshots (copied at startup):\n  GUEST\tHOST")
		if err != nil {
			return err
		}

		for index := range snapshots {
			snapshot := &snapshots[index]

			_, err = fmt.Fprintf(writer, "  %s\t%s\n", snapshot.GuestPath, snapshot.HostPath)
			if err != nil {
				return err
			}
		}

		err = writer.Flush()
		if err != nil {
			return err
		}
	}

	_, err = fmt.Fprintln(writer, "\nPublished ports:\n  HOST\tGUEST\tPROTOCOL")
	if err != nil {
		return err
	}

	if len(ports) == 0 {
		_, err = fmt.Fprintln(writer, "  (none)")
		if err != nil {
			return err
		}
	}

	for _, port := range ports {
		_, err = fmt.Fprintf(writer, "  %s\t%d\t%s\n", port.Host, port.Guest, port.Protocol)
		if err != nil {
			return err
		}
	}

	err = writer.Flush()
	if err != nil {
		return err
	}

	if command == "" {
		command = "(interactive shell)"
	}

	diskMode := "persistent"

	if temporary {
		diskMode = "temporary (memory-backed; changes discarded on exit)"
	}

	directory := "/"

	if len(mounts) > 0 {
		directory = mounts[0].GuestPath
	}

	_, err = fmt.Fprintf(output, "\nDisk:\n  %s\n\nCommand (in %s):\n  %s\n", diskMode, directory, command)
	return err
}
