package create

import (
	"fmt"
	"os"
	"unsafe"

	"golang.org/x/sys/windows"
)

func sizeDisk(disk *os.File, size int64) error {
	handle := windows.Handle(disk.Fd())

	var returned uint32

	err := windows.DeviceIoControl(handle, windows.FSCTL_SET_SPARSE, nil, 0, nil, 0, &returned, nil)
	if err != nil {
		return fmt.Errorf("mark sparse: %w", err)
	}

	err = disk.Truncate(size)
	if err != nil {
		return err
	}

	zeroRange := windows.FileZeroDataInformation{BeyondFinalZero: size}

	err = windows.DeviceIoControl(handle, windows.FSCTL_SET_ZERO_DATA, (*byte)(unsafe.Pointer(&zeroRange)), uint32(unsafe.Sizeof(zeroRange)), nil, 0, &returned, nil)
	if err != nil {
		return fmt.Errorf("zero sparse range: %w", err)
	}

	return nil
}
