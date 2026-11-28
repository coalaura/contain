package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"strconv"
)

type guestExitError int

type guestStatusWriter struct {
	output  io.Writer
	marker  string
	pending []byte
	status  int
}

func (status guestExitError) Error() string {
	return fmt.Sprintf("guest exited with status %d", int(status))
}

func (writer *guestStatusWriter) Write(data []byte) (int, error) {
	length := len(data)

	for len(data) > 0 {
		if len(writer.pending) == 0 {
			index := bytes.IndexByte(data, writer.marker[0])
			if index < 0 {
				index = len(data)
			}

			if index > 0 {
				written, err := writer.output.Write(data[:index])
				data = data[written:]

				if err != nil {
					return length - len(data), err
				}

				if written != index {
					return length - len(data), io.ErrShortWrite
				}

				continue
			}
		}

		character := data[0]
		writer.pending = append(writer.pending, character)
		data = data[1:]

		if len(writer.pending) <= len(writer.marker) {
			if character == writer.marker[len(writer.pending)-1] {
				continue
			}
		} else {
			value := writer.pending[len(writer.marker):]

			if character >= '0' && character <= '9' && len(value) <= 3 {
				continue
			}

			if character == '\x1f' && len(value) > 1 && len(value) <= 4 && writer.status < 0 {
				status, err := strconv.Atoi(string(value[:len(value)-1]))
				if err == nil && status <= 255 {
					writer.status = status
					writer.pending = writer.pending[:0]

					continue
				}
			}
		}

		// A mismatched candidate may end with the start of the real marker.
		keep := character == writer.marker[0]
		if keep {
			writer.pending = writer.pending[:len(writer.pending)-1]
		}

		err := writer.Flush()
		if err != nil {
			return length - len(data), err
		}

		if keep {
			writer.pending = append(writer.pending, character)
		}
	}

	return length, nil
}

func (writer *guestStatusWriter) Flush() error {
	if len(writer.pending) == 0 {
		return nil
	}

	written, err := writer.output.Write(writer.pending)
	if err != nil {
		return err
	}

	if written != len(writer.pending) {
		return io.ErrShortWrite
	}

	writer.pending = writer.pending[:0]

	return nil
}

func newGuestStatusWriter(output io.Writer, marker string) *guestStatusWriter {
	return &guestStatusWriter{
		output:  output,
		marker:  marker,
		pending: make([]byte, 0, len(marker)+4),
		status:  -1,
	}
}

func guestSessionResult(status int, failures ...error) error {
	err := errors.Join(failures...)
	if err != nil {
		return err
	}

	if status < 0 {
		return errors.New("guest shut down without reporting an exit status")
	}

	if status != 0 {
		return guestExitError(status)
	}

	return nil
}
