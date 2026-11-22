package terminal

import (
	"context"
	"encoding/binary"
	"io"
	"unicode/utf16"
	"unicode/utf8"
	"unsafe"

	"golang.org/x/sys/windows"
)

type consoleInputRecord struct {
	eventType uint16
	padding   uint16
	data      [16]byte
}

type consoleInputBuffer struct {
	bytes     [4096]byte
	count     int
	surrogate uint16
}

var readConsoleInput = windows.NewLazySystemDLL("kernel32.dll").NewProc("ReadConsoleInputW")

func (buffer *consoleInputBuffer) key(output io.Writer, event *consoleInputRecord) error {
	down := binary.LittleEndian.Uint32(event.data[0:4]) != 0
	repeats := binary.LittleEndian.Uint16(event.data[4:6])
	key := binary.LittleEndian.Uint16(event.data[6:8])
	character := binary.LittleEndian.Uint16(event.data[10:12])
	modifiers := binary.LittleEndian.Uint32(event.data[12:16])

	// Alt+numpad input can arrive on Alt release; other key-up events carry no input.
	if !down && (key != 0x12 || character == 0) {
		return nil
	}

	control := modifiers&(windows.LEFT_CTRL_PRESSED|windows.RIGHT_CTRL_PRESSED) != 0
	null := control && (key == ' ' || key == '2' || key == 0xc0)

	if character == 0 && !null {
		return nil
	}

	for range repeats {
		if buffer.surrogate != 0 {
			previous := buffer.surrogate
			buffer.surrogate = 0

			if character >= 0xdc00 && character <= 0xdfff {
				err := buffer.appendRune(output, utf16.DecodeRune(rune(previous), rune(character)))
				if err != nil {
					return err
				}

				continue
			}

			err := buffer.appendRune(output, utf8.RuneError)
			if err != nil {
				return err
			}
		}

		if character >= 0xd800 && character <= 0xdbff {
			buffer.surrogate = character

			continue
		}

		err := buffer.appendRune(output, rune(character))
		if err != nil {
			return err
		}
	}

	return nil
}

func (buffer *consoleInputBuffer) appendRune(output io.Writer, character rune) error {
	if len(buffer.bytes)-buffer.count < utf8.UTFMax {
		err := buffer.flush(output)
		if err != nil {
			return err
		}
	}

	encoded := utf8.AppendRune(buffer.bytes[:buffer.count], character)
	buffer.count = len(encoded)

	return nil
}

func (buffer *consoleInputBuffer) flush(output io.Writer) error {
	if buffer.count == 0 {
		return nil
	}

	count, err := output.Write(buffer.bytes[:buffer.count])
	if err == nil && count != buffer.count {
		err = io.ErrShortWrite
	}

	buffer.count = 0

	return err
}

func watchTerminalResize(resize *Resize) func() {
	// ReadConsoleInputW delivers resize and key events through one reader.
	return func() {}
}

func relayConsoleInput(ctx context.Context, input windows.Handle, output io.Writer, resize *Resize) error {
	var mode uint32

	err := windows.GetConsoleMode(input, &mode)
	if err != nil {
		return err
	}

	// Native mouse input with Quick Edit disabled makes ConPTY enable mouse
	// tracking even at the shell prompt. Let guest applications request it via VT.
	raw := mode &^ (windows.ENABLE_LINE_INPUT | windows.ENABLE_ECHO_INPUT | windows.ENABLE_PROCESSED_INPUT | windows.ENABLE_QUICK_EDIT_MODE | windows.ENABLE_MOUSE_INPUT)

	// VT mode preserves the character stream while keeping resize events separate.
	raw |= windows.ENABLE_VIRTUAL_TERMINAL_INPUT | windows.ENABLE_WINDOW_INPUT | windows.ENABLE_EXTENDED_FLAGS

	err = windows.SetConsoleMode(input, raw)
	if err != nil {
		return err
	}

	defer windows.SetConsoleMode(input, mode)

	canceled, err := windows.CreateEvent(nil, 1, 0, nil)
	if err != nil {
		return err
	}

	defer windows.CloseHandle(canceled)

	done := make(chan struct{})

	stop := context.AfterFunc(ctx, func() {
		windows.SetEvent(canceled)
		close(done)
	})

	defer func() {
		if !stop() {
			<-done
		}
	}()

	resize.notify()

	handles := []windows.Handle{canceled, input}

	var (
		records [128]consoleInputRecord
		buffer  consoleInputBuffer
	)

	for {
		_, err = windows.WaitForMultipleObjects(handles, false, windows.INFINITE)

		if ctx.Err() != nil {
			return ctx.Err()
		}

		if err != nil {
			return err
		}

		var count uint32

		result, _, readErr := readConsoleInput.Call(uintptr(input), uintptr(unsafe.Pointer(&records[0])), uintptr(len(records)), uintptr(unsafe.Pointer(&count)))
		if result == 0 {
			return readErr
		}

		for index := range count {
			event := &records[index]

			switch event.eventType {
			case windows.WINDOW_BUFFER_SIZE_EVENT:
				resize.notify()
			case windows.KEY_EVENT:
				err = buffer.key(output, event)
				if err != nil {
					return err
				}
			}
		}

		err = buffer.flush(output)
		if err != nil {
			return err
		}
	}
}
