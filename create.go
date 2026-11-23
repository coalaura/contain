package main

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"time"

	imagecreate "github.com/coalaura/contain/internal/create"
	"github.com/coalaura/contain/internal/network"
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

	err = buildAlpine(ctx, staging, size, command.Bool("tools"), output)
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

func buildAlpine(ctx context.Context, directory string, size int64, tools bool, output io.Writer) error {
	client := &http.Client{Timeout: 5 * time.Minute}

	log.Stepln("Resolving latest stable Alpine release...")

	branch, err := imagecreate.AlpineBranch(ctx, client)
	if err != nil {
		return err
	}

	log.Stepf("Creating ext4 disk (%d bytes)...\n", size)

	disk := filepath.Join(directory, "disk.raw")

	err = imagecreate.CreateDisk(disk, size)
	if err != nil {
		return err
	}

	machine := DefaultConfig().Machine

	machine.disk = disk
	machine.kernel = filepath.Join(directory, "netboot-kernel")
	machine.initrd = filepath.Join(directory, "netboot-initramfs")

	baseURL := imagecreate.AlpineMirror + "/" + branch
	netbootURL := baseURL + "/releases/x86_64/netboot/"

	log.Stepf("Downloading Alpine %s netboot kernel and initramfs...\n", branch)

	err = imagecreate.DownloadAlpine(ctx, client, netbootURL+"vmlinuz-virt", machine.kernel)
	if err != nil {
		return err
	}

	err = imagecreate.DownloadAlpine(ctx, client, netbootURL+"initramfs-virt", machine.initrd)
	if err != nil {
		return err
	}

	marker := "__CONTAIN_INSTALL_" + rand.Text() + ":"

	authority, err := network.LoadAuthority(directory)
	if err != nil {
		return err
	}

	err = imagecreate.AppendInstaller(machine.initrd, baseURL, marker, tools, authority.PublicCertificate())
	if err != nil {
		return fmt.Errorf("prepare installer initramfs: %w", err)
	}

	log.Stepln("Installing Alpine in OpenVMM...")

	if tools {
		log.Subln("Including common development tools and dependencies.")
	}

	err = installAlpine(ctx, &machine, baseURL, marker, output)
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

	return os.Remove(machine.initrd)
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
