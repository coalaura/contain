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

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	mounts := []GuestMount{
		{
			Tag:       "workspace",
			GuestPath: "/workspace",
			HostPath:  paths.WorkingDirectory,
		},

		// example
		{
			Tag:       "example",
			GuestPath: "/projects/example",
			HostPath:  "C:\\Users\\Public\\Downloads",
		},
	}

	err = RunOpenVMM(ctx, mounts)
	if errors.Is(err, context.Canceled) {
		log.Infoln("Session canceled.")

		return
	}

	log.MustExit(err)

	log.Infoln("Session ended.")
}
