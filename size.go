package main

import (
	"fmt"
	"strconv"
	"strings"
)

const (
	KSizeFactor = 1024
	MSizeFactor = KSizeFactor * 1024
	GSizeFactor = MSizeFactor * 1024

	MaxMemoryBytes = 32 * GSizeFactor // 32 GiB
)

func ParseSize(size string) (string, error) {
	size = strings.TrimSpace(size)

	if len(size) < 2 {
		return "", fmt.Errorf("invalid size %q", size)
	}

	var (
		unit   = size[len(size)-1]
		factor uint64
	)

	switch unit {
	case 'K':
		factor = KSizeFactor
	case 'M':
		factor = MSizeFactor
	case 'G':
		factor = GSizeFactor
	default:
		return "", fmt.Errorf("invalid size unit %q", string(unit))
	}

	amount := size[:len(size)-1]

	num, err := strconv.ParseUint(amount, 10, 64)
	if err != nil {
		return "", fmt.Errorf("invalid amount %q", amount)
	}

	if num > MaxMemoryBytes/factor {
		return "", fmt.Errorf("invalid size %q (max 32G)", size)
	}

	bt := num * factor

	var (
		out [9]byte // max is 33554432K
		b   = out[:0]
	)

	b = strconv.AppendUint(b, bt/KSizeFactor, 10)
	b = append(b, 'K')

	return string(b), nil
}
