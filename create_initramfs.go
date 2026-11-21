package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	_ "embed"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
)

type installerFile struct {
	name string
	mode int64
	body string
}

//go:embed create_guest.sh
var installerScript string

func appendInstaller(path, repository, marker string) error {
	overlay, err := installerOverlay(repository, marker)
	if err != nil {
		return err
	}

	output, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		return err
	}

	defer output.Close()

	// Linux unpacks concatenated initramfs archives. Keep Alpine's archive intact
	// and add a local apkovl for its diskless bootstrap to extract into the RAM root.
	compressed := gzip.NewWriter(output)

	err = writeInitramfsEntry(compressed, "contain.apkovl.tar.gz", 0100644, overlay)
	if err != nil {
		return err
	}

	err = writeInitramfsEntry(compressed, "TRAILER!!!", 0, nil)
	if err != nil {
		return err
	}

	err = compressed.Close()
	if err != nil {
		return err
	}

	return output.Close()
}

func installerOverlay(repository, marker string) ([]byte, error) {
	var buffer bytes.Buffer

	compressed := gzip.NewWriter(&buffer)
	archive := tar.NewWriter(compressed)

	// Windows checkouts can use CRLF, but Linux requires LF in the shebang.
	script := strings.ReplaceAll(installerScript, "\r\n", "\n")
	script = strings.ReplaceAll(script, "@STATUS_MARKER@", marker)

	files := [...]installerFile{
		{name: "contain-install.sh", mode: 0755, body: script},
		{name: "etc/apk/repositories", mode: 0644, body: repository + "/main\n" + repository + "/community\n"},
	}

	for _, entry := range files {
		header := &tar.Header{
			Name:     entry.name,
			Mode:     entry.mode,
			Size:     int64(len(entry.body)),
			Typeflag: tar.TypeReg,
		}

		err := archive.WriteHeader(header)
		if err != nil {
			return nil, err
		}

		_, err = io.WriteString(archive, entry.body)
		if err != nil {
			return nil, err
		}
	}

	err := archive.Close()
	closeErr := compressed.Close()

	if err != nil || closeErr != nil {
		return nil, errors.Join(err, closeErr)
	}

	return buffer.Bytes(), nil
}

func writeInitramfsEntry(output io.Writer, name string, mode uint32, data []byte) error {
	// A newc header is 110 bytes; both the NUL-terminated name and body end on
	// four-byte boundaries. Only the apkovl and trailer are needed in this archive.
	_, err := fmt.Fprintf(output, "070701%08x%08x%08x%08x%08x%08x%08x%08x%08x%08x%08x%08x%08x", 1, mode, 0, 0, 1, 0, len(data), 0, 0, 0, 0, len(name)+1, 0)
	if err != nil {
		return err
	}

	_, err = io.WriteString(output, name)
	if err != nil {
		return err
	}

	var padding [4]byte

	_, err = output.Write(padding[:1+(-(110+len(name)+1)&3)])
	if err != nil {
		return err
	}

	_, err = output.Write(data)
	if err != nil {
		return err
	}

	_, err = output.Write(padding[:(-len(data))&3])
	return err
}
