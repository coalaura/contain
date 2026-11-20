package main

import "github.com/coalaura/plain/minimal"

var (
	paths  *Paths
	config *Config

	log = minimal.New()
)

func main() {
	var err error

	log.Infoln("Resolving paths...")

	paths, err = ResolvePaths()
	log.MustFail(err)

	log.Infoln("Loading config...")

	config, err = LoadConfig()
	log.MustFail(err)

	log.Infoln("Starting VM...")

	err = RunOpenVMM()
	log.MustFail(err)
}
