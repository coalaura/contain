package create

import (
	"fmt"
	"slices"
	"strings"
)

const DefaultKernel = "virt"

var kernelVariants = [...]string{DefaultKernel, "lts", "stable"}

func ValidateKernel(kernel string) error {
	if !slices.Contains(kernelVariants[:], kernel) {
		return fmt.Errorf("unsupported kernel %q (choose %s)", kernel, strings.Join(kernelVariants[:], ", "))
	}

	return nil
}
