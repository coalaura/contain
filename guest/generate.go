//go:build ignore

package main

import (
	"fmt"
	"os"
	"os/exec"
)

func main() {
	command := exec.Command("go", "build", "-trimpath", "-buildvcs=false", "-ldflags=-s -w", "-o", "guest/resize", "./guest")

	command.Env = append(os.Environ(), "GOOS=linux", "GOARCH=amd64", "GOAMD64=v1", "CGO_ENABLED=0")
	command.Stdout = os.Stdout
	command.Stderr = os.Stderr

	err := command.Run()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)

		os.Exit(1)
	}
}
