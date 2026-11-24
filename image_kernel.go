package main

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"os"
)

func imageKernelVersion(path string) string {
	file, err := os.Open(path)
	if err != nil {
		return "unknown"
	}

	defer file.Close()

	var header [0x210]byte

	_, err = io.ReadFull(file, header[:])
	if err != nil {
		return "unknown"
	}

	if binary.LittleEndian.Uint16(header[0x1fe:]) != 0xaa55 || string(header[0x202:0x206]) != "HdrS" || binary.LittleEndian.Uint16(header[0x206:]) < 0x200 {
		return "unknown"
	}

	// The x86 boot protocol stores kernel_version relative to the start of the setup code at 0x200.
	offset := binary.LittleEndian.Uint16(header[0x20e:])
	if offset == 0 {
		return "unknown"
	}

	var version [256]byte

	count, err := file.ReadAt(version[:], 0x200+int64(offset))
	if err != nil && !errors.Is(err, io.EOF) {
		return "unknown"
	}

	// The release precedes optional build information in the NUL-terminated version string.
	end := bytes.IndexAny(version[:count], " \x00")
	if end <= 0 {
		return "unknown"
	}

	for _, character := range version[:end] {
		if character < '!' || character > '~' {
			return "unknown"
		}
	}

	return string(version[:end])
}
