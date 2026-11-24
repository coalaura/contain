package main

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"time"

	imagecreate "github.com/coalaura/contain/internal/create"
	"github.com/coalaura/contain/internal/sessionlog"
	"github.com/urfave/cli/v3"
)

func create(ctx context.Context, command *cli.Command) error {
	if command.Args().Present() {
		return fmt.Errorf("unexpected argument %q", command.Args().First())
	}

	if runtime.GOARCH != "amd64" {
		return errors.New("create currently requires an amd64 host")
	}

	name := command.String("image")

	err := validateImageName(name)
	if err != nil {
		return err
	}

	size, err := imagecreate.ParseDiskSize(command.String("size"))
	if err != nil {
		return fmt.Errorf("--size: %w", err)
	}

	kernel := command.String("kernel")

	err = imagecreate.ValidateKernel(kernel)
	if err != nil {
		return fmt.Errorf("--kernel: %w", err)
	}

	start := time.Now()
	sessionID := sessionlog.NewID()

	paths, err = ResolvePaths()
	if err != nil {
		return err
	}

	directory := filepath.Join(paths.ImagesDirectory, name)

	err = requireNewImage(directory)
	if err != nil {
		return err
	}

	err = os.MkdirAll(filepath.Dir(directory), 0700)
	if err != nil {
		return err
	}

	staging, err := os.MkdirTemp(filepath.Dir(directory), ".image-*")
	if err != nil {
		return err
	}

	defer os.RemoveAll(staging)

	logConfig := sessionlog.Config{
		MaxSizeMiB: sessionlog.DefaultMaxSizeMiB,
	}

	output, err := sessionlog.New(paths.LogsDirectory, "contain-create-"+sessionID+".log", logConfig.MaxBytes())
	if err != nil {
		return fmt.Errorf("create installer log: %w", err)
	}

	log.Subf("Installer log: %s\n", output.Name())

	err = buildAlpine(ctx, staging, size, kernel, command.Bool("tools"), output)
	if err != nil {
		fmt.Fprintln(output, err)
	}

	closeErr := output.Close()

	err = errors.Join(err, closeErr)
	if err != nil {
		return fmt.Errorf("create Alpine image (log: %s): %w", output.Name(), err)
	}

	err = ctx.Err()
	if err != nil {
		return err
	}

	err = requireNewImage(directory)
	if err != nil {
		return err
	}

	err = os.Rename(staging, directory)
	if err != nil {
		return fmt.Errorf("publish Alpine image: %w", err)
	}

	took := time.Since(start).Round(time.Millisecond)

	log.Successf("Image %q created in %s.\n", name, took)

	log.Subf("Image: %s\n", directory)
	log.Subln("Login: root / alpine")
	log.Subf("Run with: contain --image %s\n", name)

	return nil
}

func buildAlpine(ctx context.Context, directory string, size int64, kernel string, tools bool, output io.Writer) error {
	machine := DefaultConfig().Machine

	machine.disk = filepath.Join(directory, "disk.raw")
	machine.kernel = filepath.Join(directory, "netboot-kernel")
	machine.initrd = filepath.Join(directory, "netboot-initramfs")

	modloop := filepath.Join(directory, "netboot-modloop")

	baseURL, publicCA, err := prepareAlpine(ctx, directory, size, &machine, modloop)
	if err != nil {
		return err
	}

	marker := "__CONTAIN_INSTALL_" + rand.Text() + ":"

	err = imagecreate.AppendInstaller(machine.initrd, baseURL, marker, kernel, tools, publicCA)
	if err != nil {
		return fmt.Errorf("prepare installer initramfs: %w", err)
	}

	log.Stepf("Installing Alpine with linux-%s in OpenVMM...\n", kernel)

	if tools {
		log.Subln("Including common development tools and dependencies.")
	}

	err = installAlpine(ctx, &machine, modloop, marker, output)
	if err != nil {
		return err
	}

	log.Stepln("Finalizing installed kernel and initramfs...")

	err = imagecreate.CollectBootFiles(directory)
	if err != nil {
		return err
	}

	err = os.Remove(machine.kernel)
	if err != nil {
		return err
	}

	err = os.Remove(machine.initrd)
	if err != nil {
		return err
	}

	return os.Remove(modloop)
}

func requireNewImage(directory string) error {
	_, err := os.Lstat(directory)
	if err == nil {
		return fmt.Errorf("image destination already exists: %s", directory)
	}

	if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("check image destination: %w", err)
	}

	return nil
}
