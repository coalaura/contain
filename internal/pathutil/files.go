package pathutil

import (
	"fmt"
	"os"
)

func FilesExist(files ...string) error {
	for _, file := range files {
		stat, err := os.Stat(file)
		if os.IsNotExist(err) {
			return fmt.Errorf("%q does not exist", file)
		} else if err != nil {
			return err
		}

		if stat.IsDir() {
			return fmt.Errorf("%q is a directory", file)
		} else if stat.Size() == 0 {
			return fmt.Errorf("%q is empty", file)
		}
	}

	return nil
}
