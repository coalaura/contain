package network

import (
	"errors"
	"os"

	"golang.org/x/sys/windows"
)

func protectNetworkDirectory(directory string) error {
	info, err := os.Lstat(directory)
	if err != nil {
		return err
	}

	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("network private directory must not be a symlink")
	}

	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return err
	}

	descriptor, err := windows.SecurityDescriptorFromString("D:P(A;OICI;FA;;;" + user.User.Sid.String() + ")")
	if err != nil {
		return err
	}

	acl, _, err := descriptor.DACL()
	if err != nil {
		return err
	}

	return windows.SetNamedSecurityInfo(directory, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, acl, nil)
}
