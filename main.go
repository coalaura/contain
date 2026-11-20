package main

import (
	"context"
	"errors"
	"os"
	"os/signal"
	"syscall"

	"github.com/coalaura/plain/minimal"
)

var (
	paths  *Paths
	config *Config

	log = minimal.New(minimal.ToStdErr())
)

func main() {
	var err error

	log.Stepln("Resolving paths...")

	paths, err = ResolvePaths()
	log.MustFail(err)

	log.Stepln("Loading configuration...")

	config, err = LoadConfig()
	log.MustFail(err)

	mounts, err := config.GuestMounts(paths.WorkingDirectory)
	log.MustExit(err)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	err = RunOpenVMM(ctx, mounts)
	if errors.Is(err, context.Canceled) {
		log.Infoln("Session canceled.")

		return
	}

	log.MustExit(err)

	log.Writeln("")
	log.Infoln("Session ended.")
}
