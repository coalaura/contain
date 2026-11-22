package sessionlog

import (
	"errors"
	"io"
	"os"
	"strings"
	"sync"
)

const DefaultMaxSizeMiB = 10

type Config struct {
	MaxSizeMiB int64 `yaml:"max_size_mib"`
}

type Writer struct {
	mutex sync.Mutex
	file *os.File
	name string
	previous string
	limit int64
	size int64
	failure error
}

func (config Config) Validate() error {
	if config.MaxSizeMiB < 1 || config.MaxSizeMiB > 1024 {
		return errors.New("logs.max_size_mib must be between 1 and 1024")
	}

	return nil
}

func (config Config) MaxBytes() int64 {
	return config.MaxSizeMiB * 1024 * 1024
}

func (writer *Writer) Name() string {
	return writer.name
}

func (writer *Writer) Write(data []byte) (int, error) {
	writer.mutex.Lock()
	defer writer.mutex.Unlock()

	if writer.failure != nil {
		return 0, writer.failure
	}

	if writer.file == nil {
		return 0, os.ErrClosed
	}

	written := 0

	for len(data) > 0 {
		if writer.size == writer.limit {
			writer.failure = writer.rotate()
			if writer.failure != nil {
				return written, writer.failure
			}
		}

		length := int(min(int64(len(data)), writer.limit-writer.size))
		count, err := writer.file.Write(data[:length])
		writer.size += int64(count)
		written += count
		data = data[count:]

		if err == nil && count != length {
			err = io.ErrShortWrite
		}

		if err != nil {
			writer.failure = err

			return written, err
		}
	}

	return written, nil
}

func (writer *Writer) Close() error {
	writer.mutex.Lock()
	defer writer.mutex.Unlock()

	if writer.file != nil {
		writer.failure = errors.Join(writer.failure, writer.file.Close())
		writer.file = nil
	}

	return writer.failure
}

func (writer *Writer) rotate() error {
	err := writer.file.Close()
	writer.file = nil

	if err != nil {
		return err
	}

	err = os.Remove(writer.previous)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}

	err = os.Rename(writer.name, writer.previous)
	if err != nil {
		return err
	}

	// Exclusive creation avoids following a replacement symlink at the active log path.
	writer.file, err = os.OpenFile(writer.name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}

	writer.size = 0

	return nil
}

func New(directory, pattern string, limit int64) (*Writer, error) {
	if limit < 1 {
		return nil, errors.New("session log size limit must be positive")
	}

	file, err := os.CreateTemp(directory, pattern)
	if err != nil {
		return nil, err
	}

	name := file.Name()

	return &Writer{
		file: file,
		name: name,
		previous: strings.TrimSuffix(name, ".log") + ".1.log",
		limit: limit,
	}, nil
}
