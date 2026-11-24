package create

import (
	"compress/gzip"
	_ "embed"
	"fmt"
	"io"
	"os"
	"strings"
)

type installerFile struct {
	name string
	mode uint32
	body string
}

//go:embed installer.sh
var installerScript string

//go:embed tools.sh
var toolsScript string

func AppendInstaller(path, repository, marker, kernel string, tools bool, publicCA []byte) error {
	files, err := installerFiles(repository, marker, kernel, tools, publicCA)
	if err != nil {
		return err
	}

	output, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		return err
	}

	defer output.Close()

	// Linux unpacks concatenated initramfs archives, exposing our rdinit directly
	// alongside Alpine's existing tools, keys and early drivers.
	compressed := gzip.NewWriter(output)

	for _, entry := range files {
		err = writeInitramfsEntry(compressed, entry.name, entry.mode, entry.body)
		if err != nil {
			return err
		}
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

func installerFiles(repository, marker, kernel string, tools bool, publicCA []byte) ([]installerFile, error) {
	err := ValidateKernel(kernel)
	if err != nil {
		return nil, err
	}

	packageName := "linux-" + kernel
	repositories := repository + "/main\n" + repository + "/community\n"

	if kernel == "stable" {
		// Tag edge so the base system and tools still use the stable release.
		packageName += "@edge"
		repositories += "@edge " + AlpineMirror + "/edge/community\n"
	}

	// Windows checkouts can use CRLF, but Linux requires LF in the shebang.
	script := strings.ReplaceAll(installerScript, "\r\n", "\n")
	script = strings.ReplaceAll(script, "@STATUS_MARKER@", marker)
	script = strings.ReplaceAll(script, "@KERNEL@", kernel)
	script = strings.ReplaceAll(script, "@KERNEL_PACKAGE@", packageName)

	files := make([]installerFile, 0, 4)
	files = append(files, installerFile{name: "contain.crt", mode: 0100644, body: string(publicCA)})
	files = append(files, installerFile{name: "contain-install.sh", mode: 0100755, body: script})
	files = append(files, installerFile{name: "etc/apk/repositories", mode: 0100644, body: repositories})

	if tools {
		body := strings.ReplaceAll(toolsScript, "\r\n", "\n")

		files = append(files, installerFile{name: "contain-tools.sh", mode: 0100755, body: body})
	}

	return files, nil
}

func writeInitramfsEntry(output io.Writer, name string, mode uint32, data string) error {
	return writeInitramfsFile(output, name, mode, int64(len(data)), strings.NewReader(data))
}

func writeInitramfsFile(output io.Writer, name string, mode uint32, size int64, data io.Reader) error {
	// A newc header is 110 bytes; both the NUL-terminated name and body end on
	// four-byte boundaries.
	_, err := fmt.Fprintf(output, "070701%08x%08x%08x%08x%08x%08x%08x%08x%08x%08x%08x%08x%08x", 1, mode, 0, 0, 1, 0, size, 0, 0, 0, 0, len(name)+1, 0)
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

	_, err = io.CopyN(output, data, size)
	if err != nil {
		return err
	}

	_, err = output.Write(padding[:(-size)&3])
	return err
}
