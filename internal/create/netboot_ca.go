package create

import (
	"archive/tar"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/diskfs/go-diskfs/filesystem/iso9660"
)

func appendNetbootCA(filesystem *iso9660.FileSystem, initrd string) error {
	entries, err := filesystem.ReadDir("apks/x86_64")
	if err != nil {
		return fmt.Errorf("list Alpine ISO packages: %w", err)
	}

	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasPrefix(name, "ca-certificates-bundle-") || !strings.HasSuffix(name, ".apk") {
			continue
		}

		err = appendNetbootCAFile(filesystem, initrd, name)
		if err != nil {
			return fmt.Errorf("extract installer trust bundle: %w", err)
		}

		return nil
	}

	return errors.New("alpine ISO contains no CA bundle package")
}

func appendNetbootCAFile(filesystem *iso9660.FileSystem, initrd, name string) error {
	input, err := filesystem.OpenFile("/apks/x86_64/"+name, os.O_RDONLY)
	if err != nil {
		return err
	}

	defer input.Close()

	compressed, err := gzip.NewReader(input)
	if err != nil {
		return err
	}

	defer compressed.Close()

	// APK v2 concatenates gzip members containing signature, metadata and data tar records.
	archive := tar.NewReader(io.LimitReader(compressed, 4<<20))

	for {
		header, err := archive.Next()
		if err != nil {
			return fmt.Errorf("locate CA bundle in %s: %w", name, err)
		}

		if strings.TrimPrefix(header.Name, "./") != "etc/ssl/certs/ca-certificates.crt" {
			continue
		}

		if header.Typeflag != tar.TypeReg || header.Size <= 0 || header.Size > 1<<20 {
			return errors.New("invalid Alpine CA bundle entry")
		}

		return appendTrustBundle(initrd, archive, header.Size)
	}
}

func appendTrustBundle(initrd string, bundle io.Reader, size int64) error {
	output, err := os.OpenFile(initrd, os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		return err
	}

	defer output.Close()

	compressed := gzip.NewWriter(output)
	directories := [...]string{"etc/ssl", "etc/ssl/certs"}

	for _, directory := range directories {
		err = writeInitramfsEntry(compressed, directory, 0040755, "")
		if err != nil {
			return err
		}
	}

	err = writeInitramfsFile(compressed, "etc/ssl/certs/ca-certificates.crt", 0100644, size, bundle)
	if err != nil {
		return err
	}

	err = writeInitramfsEntry(compressed, "etc/ssl/cert.pem", 0120777, "certs/ca-certificates.crt")
	if err != nil {
		return err
	}

	err = writeInitramfsEntry(compressed, "TRAILER!!!", 0, "")
	if err != nil {
		return err
	}

	err = compressed.Close()
	if err != nil {
		return err
	}

	return output.Close()
}
