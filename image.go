package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"text/tabwriter"

	"github.com/urfave/cli/v3"
)

const defaultImageName = "alpine"

func listImages(ctx context.Context, command *cli.Command) error {
	if command.Args().Present() {
		return fmt.Errorf("unexpected argument %q", command.Args().First())
	}

	directory, err := imagesDirectory()
	if err != nil {
		return err
	}

	entries, err := os.ReadDir(directory)
	if errors.Is(err, os.ErrNotExist) {
		_, err = fmt.Fprintln(command.Writer, "No images found.")
		return err
	}

	if err != nil {
		return fmt.Errorf("list images: %w", err)
	}

	writer := tabwriter.NewWriter(command.Writer, 0, 4, 2, ' ', 0)
	count := 0

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}

		name := entry.Name()

		err = validateImageName(name)
		if err != nil {
			continue
		}

		size, err := imageSize(ctx, filepath.Join(directory, name))
		if err != nil {
			return fmt.Errorf("image %q: %w", name, err)
		}

		if count == 0 {
			_, err = fmt.Fprintln(writer, "IMAGE\tSIZE\tKERNEL")
			if err != nil {
				return err
			}
		}

		kernel := imageKernelVersion(filepath.Join(directory, name, "vmlinuz-virt"))

		_, err = fmt.Fprintf(writer, "%s\t%s\t%s\n", name, formatImageSize(size), kernel)
		if err != nil {
			return err
		}

		count++
	}

	if count == 0 {
		_, err = fmt.Fprintln(writer, "No images found.")
		if err != nil {
			return err
		}
	}

	return writer.Flush()
}

func removeImage(ctx context.Context, command *cli.Command) error {
	if command.NArg() != 1 {
		return errors.New("expected one image name: contain remove NAME")
	}

	name := command.Args().First()

	err := validateImageName(name)
	if err != nil {
		return err
	}

	directory, err := imagesDirectory()
	if err != nil {
		return err
	}

	root, err := os.OpenRoot(directory)
	if err != nil {
		return fmt.Errorf("open image store: %w", err)
	}

	defer root.Close()

	info, err := root.Lstat(name)
	if err != nil {
		return fmt.Errorf("image %q: %w", name, err)
	}

	if !info.IsDir() {
		return fmt.Errorf("image %q is not a directory", name)
	}

	err = confirmImageRemoval(ctx, command.Reader, command.Writer, name)
	if err != nil {
		return err
	}

	current, err := root.Lstat(name)
	if err != nil {
		return err
	}

	if !os.SameFile(info, current) {
		return errors.New("image changed during confirmation; removal canceled")
	}

	err = ctx.Err()
	if err != nil {
		return err
	}

	err = root.RemoveAll(name)
	if err != nil {
		return fmt.Errorf("remove image %q: %w", name, err)
	}

	_, err = fmt.Fprintf(command.Writer, "Removed image %q.\n", name)
	return err
}

func imageFlag() *cli.StringFlag {
	return &cli.StringFlag{
		Name:    "image",
		Aliases: []string{"i"},
		Usage:   "Image `NAME` in ~/.local/contain/images",
		Value:   defaultImageName,
	}
}

func validateImageName(name string) error {
	if name == "" || len(name) > 255 || !filepath.IsLocal(name) {
		return fmt.Errorf("invalid image name %q", name)
	}

	for index := range len(name) {
		character := name[index]

		letter := character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z'
		digit := character >= '0' && character <= '9'
		separator := index > 0 && (character == '-' || character == '_')

		if !letter && !digit && !separator {
			return fmt.Errorf("invalid image name %q: use letters, digits, hyphens or underscores, starting with a letter or digit", name)
		}
	}

	return nil
}

func imagesDirectory() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}

	return filepath.Join(home, ".local", "contain", "images"), nil
}

func imageSize(ctx context.Context, directory string) (int64, error) {
	var size int64

	err := filepath.WalkDir(directory, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		err = ctx.Err()
		if err != nil {
			return err
		}

		if !entry.Type().IsRegular() {
			return nil
		}

		info, err := entry.Info()
		if err != nil {
			return err
		}

		size += info.Size()

		return nil
	})

	return size, err
}

func formatImageSize(size int64) string {
	if size < 1024 {
		return fmt.Sprintf("%d B", size)
	}

	units := [...]string{"KiB", "MiB", "GiB", "TiB", "PiB", "EiB"}
	amount := float64(size) / 1024
	unit := 0

	for amount >= 1024 && unit < len(units)-1 {
		amount /= 1024
		unit++
	}

	return fmt.Sprintf("%.2f %s", amount, units[unit])
}

func confirmImageRemoval(ctx context.Context, input io.Reader, output io.Writer, name string) error {
	err := ctx.Err()
	if err != nil {
		return err
	}

	_, err = fmt.Fprintf(output, "Permanently delete image %q and all its files?\nType %q to confirm: ", name, name)
	if err != nil {
		return err
	}

	result := make(chan error, 1)

	go func() {
		scanner := bufio.NewScanner(input)
		if !scanner.Scan() {
			err := scanner.Err()
			if err != nil {
				result <- fmt.Errorf("read confirmation: %w", err)

				return
			}

			result <- errors.New("no confirmation received; removal canceled")

			return
		}

		if scanner.Text() != name {
			result <- errors.New("image name did not match; removal canceled")

			return
		}

		result <- nil
	}()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case err = <-result:
		return err
	}
}
