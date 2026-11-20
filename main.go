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

	log = minimal.New()
)

func main() {
	var err error

	paths, err = ResolvePaths()
	log.MustFail(err)

	config, err = LoadConfig()
	log.MustFail(err)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	err = RunOpenVMM(ctx)
	if errors.Is(err, context.Canceled) {
		return
	}

	log.MustFail(err)
}
