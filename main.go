package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"

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
		Name:            "contain",
		Usage:           "Run tools and agents in an isolated Linux workspace",
		UsageText:       "contain [options]",
		Version:         version,
		HideHelpCommand: true,
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

	mounts, err := config.GuestMounts(paths.WorkingDirectory)
	if err != nil {
		return err
	}

	startupCommand := config.Machine.Command

	if command.IsSet("run") {
		startupCommand = command.String("run")
	} else if command.Bool("shell") {
		startupCommand = ""
	}

	err = RunOpenVMM(ctx, mounts, startupCommand)
	if err != nil {
		return err
	}

	log.Writeln("")
	log.Infoln("Session ended.")

	return nil
}
