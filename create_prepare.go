package main

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"time"

	imagecreate "github.com/coalaura/contain/internal/create"
	"github.com/coalaura/contain/internal/network"
)

func prepareAlpine(ctx context.Context, directory string, size int64, machine *MachineConfig) (string, []byte, error) {
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

	// Fixed fan-out: disk, CA and release resolution, then at most two downloads.
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
		branch, err := imagecreate.AlpineBranch(ctx, client)
		if err != nil {
			cancel(err)

			return
		}

		baseURL = imagecreate.AlpineMirror + "/" + branch
		netbootURL := baseURL + "/releases/x86_64/netboot/"

		log.Stepf("Downloading Alpine %s netboot kernel and initramfs...\n", branch)

		// The installer always boots virt; the installed image uses the selected kernel.
		workers.Go(func() {
			err := imagecreate.DownloadAlpine(ctx, client, netbootURL+"vmlinuz-virt", machine.kernel)
			if err != nil {
				cancel(err)
			}
		})

		err = imagecreate.DownloadAlpine(ctx, client, netbootURL+"initramfs-virt", machine.initrd)
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
