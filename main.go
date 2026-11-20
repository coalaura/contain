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
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	cmd := &cli.Command{
		Name:                      "contain",
		Usage:                     "Run tools and agents in an isolated Linux workspace",
		UsageText:                 "contain [options]",
		Version:                   version,
		HideHelpCommand:           true,
		DisableSliceFlagSeparator: true,
		Flags: []cli.Flag{
			&cli.BoolFlag{
				Name:  "shell",
				Usage: "Start a shell instead of the configured command",
			},
			&cli.StringFlag{
				Name:      "run",
				Usage:     "Run `command` instead of the configured command",
				Validator: validateShellCommand,
			},
			&cli.StringSliceFlag{
				Name:  "mount",
				Usage: "Add a session mount `HOST[:NAME][:ro|rw]` (repeatable; name defaults to the directory basename)",
			},
			&cli.BoolFlag{
				Name:    "dry-run",
				Aliases: []string{"dry"},
				Usage:   "Print the resolved mounts and command without starting the VM",
			},
		},
		Action: run,
	}

	err := cmd.Run(ctx, os.Args)
	if errors.Is(err, context.Canceled) {
		log.Infoln("Session canceled.")

		return
	}

	log.MustExit(err)
}

func run(ctx context.Context, command *cli.Command) error {
	if command.Args().Present() {
		return fmt.Errorf("unexpected argument %q", command.Args().First())
	}

	if command.Bool("shell") && command.IsSet("run") {
		return errors.New("--shell and --run cannot be used together")
	}

	var err error

	mountValues := command.StringSlice("mount")
	additionalMounts := make([]MountConfig, len(mountValues))

	for index, value := range mountValues {
		additionalMounts[index], err = parseMount(value)
		if err != nil {
			return fmt.Errorf("--mount %q: %w", value, err)
		}
	}

	log.Stepln("Resolving paths...")

	paths, err = ResolvePaths()
	if err != nil {
		return err
	}

	log.Stepln("Loading configuration...")

	config, err = LoadConfig()
	if err != nil {
		return err
	}

	mounts, err := config.GuestMounts(paths.WorkingDirectory, additionalMounts...)
	if err != nil {
		return err
	}

	startupCommand := config.Machine.Command

	if command.IsSet("run") {
		startupCommand = command.String("run")
	} else if command.Bool("shell") {
		startupCommand = ""
	}

	if command.Bool("dry-run") {
		return printDryRun(command.Writer, mounts, startupCommand)
	}

	err = RunOpenVMM(ctx, mounts, startupCommand)
	if err != nil {
		return err
	}

	log.Writeln("")
	log.Infoln("Session ended.")

	return nil
}

func printDryRun(output io.Writer, mounts []GuestMount, command string) error {
	writer := tabwriter.NewWriter(output, 0, 4, 2, ' ', 0)

	_, err := fmt.Fprintln(writer, "Mounts:\n  GUEST\tACCESS\tHOST")
	if err != nil {
		return err
	}

	for index := range mounts {
		mount := &mounts[index]
		access := "read/write"

		if mount.Readonly {
			access = "read-only"
		}

		_, err = fmt.Fprintf(writer, "  %s\t%s\t%s\n", mount.GuestPath, access, mount.HostPath)
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

	_, err = fmt.Fprintf(output, "\nCommand (in /workspace):\n  %s\n", command)
	return err
}
