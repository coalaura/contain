package main

import (
	"io/fs"
	"os"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Layout of Windows FILE_COMPRESSION_INFO, including its reserved trailing bytes.
type fileCompressionInformation struct {
	compressedFileSize   int64
	compressionFormat    uint16
	compressionUnitShift uint8
	chunkShift           uint8
	clusterShift         uint8
	reserved             [3]byte
}

func allocatedFileSize(path string, _ fs.FileInfo) (int64, error) {
	file, err := os.Open(path)
	if err != nil {
		return 0, err
	}

	defer file.Close()

	var info fileCompressionInformation

	// CompressedFileSize also reports actual allocation for sparse, uncompressed files.
	err = windows.GetFileInformationByHandleEx(windows.Handle(file.Fd()), windows.FileCompressionInfo, (*byte)(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info)))
	if err != nil {
		return 0, err
	}

	return info.compressedFileSize, nil
}
