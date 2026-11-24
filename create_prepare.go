package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	imagecreate "github.com/coalaura/contain/internal/create"
	"github.com/coalaura/contain/internal/network"
)

func prepareAlpine(ctx context.Context, directory string, size int64, machine *MachineConfig, modloop string) (string, []byte, error) {
	err := ctx.Err()
	if err != nil {
		return "", nil, err
	}

	ctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)

	client := &http.Client{Timeout: 5 * time.Minute}

	var (
		workers  sync.WaitGroup
		baseURL  string
		publicCA []byte
	)

	log.Stepf("Creating ext4 disk (%d bytes) and private network CA...\n", size)
	log.Stepln("Resolving latest stable Alpine release...")

	// Fixed fan-out: disk and CA work alongside at most three ISO range downloads.
	workers.Go(func() {
		err := imagecreate.CreateDisk(machine.disk, size)
		if err != nil {
			cancel(err)
		}
	})

	workers.Go(func() {
		authority, err := network.LoadAuthority(directory)
		if err != nil {
			cancel(fmt.Errorf("prepare network CA: %w", err))

			return
		}

		publicCA = authority.PublicCertificate()
	})

	workers.Go(func() {
		release, err := imagecreate.ResolveAlpine(ctx, client)
		if err != nil {
			cancel(err)

			return
		}

		baseURL = imagecreate.AlpineMirror + "/" + release.Branch
		archive := filepath.Join(directory, "netboot.iso")

		log.Stepf("Downloading Alpine %s installer kernel, initramfs and modloop...\n", release.Version)

		err = imagecreate.DownloadAlpine(ctx, client, &release, archive)
		if err != nil {
			cancel(err)

			return
		}

		err = imagecreate.ExtractNetboot(archive, machine.kernel, machine.initrd, modloop)
		if err != nil {
			cancel(err)

			return
		}

		err = os.Remove(archive)
		if err != nil {
			cancel(err)
		}
	})

	// Formatting and CA creation cannot be interrupted; join before staging cleanup.
	workers.Wait()

	err = context.Cause(ctx)
	if err != nil {
		return "", nil, err
	}

	return baseURL, publicCA, nil
}
