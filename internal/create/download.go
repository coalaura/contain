package create

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"

	"github.com/goccy/go-yaml"
)

const (
	AlpineMirror   = "https://dl-cdn.alpinelinux.org/alpine"
	maxNetbootSize = 256 << 20
	downloadParts  = 3
)

type AlpineRelease struct {
	Branch  string `yaml:"branch"`
	Arch    string `yaml:"arch"`
	Flavor  string `yaml:"flavor"`
	Version string `yaml:"version"`
	File    string `yaml:"file"`
	Size    int64  `yaml:"size"`
	SHA256  string `yaml:"sha256"`
}

func ResolveAlpine(ctx context.Context, client *http.Client) (AlpineRelease, error) {
	url := AlpineMirror + "/latest-stable/releases/x86_64/latest-releases.yaml"

	response, err := requestAlpine(ctx, client, url)
	if err != nil {
		return AlpineRelease{}, err
	}

	defer response.Body.Close()

	data, err := io.ReadAll(io.LimitReader(response.Body, (1<<20)+1))
	if err != nil {
		return AlpineRelease{}, err
	}

	if len(data) > 1<<20 {
		return AlpineRelease{}, errors.New("release metadata exceeds 1 MiB")
	}

	var releases []AlpineRelease

	err = yaml.Unmarshal(data, &releases)
	if err != nil {
		return AlpineRelease{}, fmt.Errorf("decode Alpine releases: %w", err)
	}

	for index := range releases {
		release := &releases[index]
		if release.Arch != "x86_64" || release.Flavor != "alpine-virt" {
			continue
		}

		err = validateAlpineRelease(release)
		if err != nil {
			return AlpineRelease{}, err
		}

		return *release, nil
	}

	return AlpineRelease{}, errors.New("release metadata contains no stable x86_64 Alpine virtual release")
}

func DownloadAlpine(ctx context.Context, client *http.Client, release *AlpineRelease, destination string) error {
	err := validateAlpineRelease(release)
	if err != nil {
		return err
	}

	output, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
	if err != nil {
		return err
	}

	defer output.Close()

	err = output.Truncate(release.Size)
	if err != nil {
		return err
	}

	ctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)

	url := AlpineMirror + "/" + release.Branch + "/releases/x86_64/" + release.File

	var workers sync.WaitGroup

	// Disjoint ranges stream directly into one file, with a fixed three-request bound.
	for part := range downloadParts {
		offset := release.Size * int64(part) / downloadParts
		length := release.Size*int64(part+1)/downloadParts - offset

		workers.Go(func() {
			err := downloadAlpineRange(ctx, client, url, output, offset, length, release.Size)
			if err != nil {
				cancel(err)
			}
		})
	}

	workers.Wait()

	err = context.Cause(ctx)
	if err != nil {
		return err
	}

	// Verify the complete versioned ISO before extracting any boot artifacts.
	hash := sha256.New()

	_, err = io.Copy(hash, output)
	if err != nil {
		return fmt.Errorf("hash Alpine ISO: %w", err)
	}

	digest := hex.EncodeToString(hash.Sum(nil))
	if !strings.EqualFold(digest, release.SHA256) {
		return fmt.Errorf("alpine ISO SHA-256 mismatch: got %s, want %s", digest, release.SHA256)
	}

	return output.Close()
}

func downloadAlpineRange(ctx context.Context, client *http.Client, url string, output *os.File, offset, length, size int64) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}

	end := offset + length - 1

	request.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", offset, end))
	request.Header.Set("Accept-Encoding", "identity")

	response, err := client.Do(request)
	if err != nil {
		return fmt.Errorf("download Alpine ISO range: %w", err)
	}

	defer response.Body.Close()

	expectedRange := fmt.Sprintf("bytes %d-%d/%d", offset, end, size)
	if response.StatusCode != http.StatusPartialContent || response.Header.Get("Content-Range") != expectedRange {
		return fmt.Errorf("download Alpine ISO range %s: unexpected response %s (%s)", expectedRange, response.Status, response.Header.Get("Content-Range"))
	}

	if response.ContentLength >= 0 && response.ContentLength != length {
		return fmt.Errorf("download Alpine ISO range: expected %d bytes, got %d", length, response.ContentLength)
	}

	writer := io.NewOffsetWriter(output, offset)

	written, err := io.Copy(writer, io.LimitReader(response.Body, length))
	if err != nil {
		return fmt.Errorf("download Alpine ISO range: %w", err)
	}

	if written != length {
		return fmt.Errorf("download Alpine ISO range: expected %d bytes, got %d", length, written)
	}

	var extra [1]byte

	_, err = io.ReadFull(response.Body, extra[:])
	if !errors.Is(err, io.EOF) {
		return fmt.Errorf("download Alpine ISO range: unexpected data after %d bytes: %v", length, err)
	}

	return nil
}

func requestAlpine(ctx context.Context, client *http.Client, url string) (*http.Response, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}

	response, err := client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("download %s: %w", url, err)
	}

	if response.StatusCode != http.StatusOK {
		response.Body.Close()

		return nil, fmt.Errorf("download %s: %s", url, response.Status)
	}

	return response, nil
}

func validAlpineBranch(branch string) bool {
	if !strings.HasPrefix(branch, "v") {
		return false
	}

	major, minor, found := strings.Cut(branch[1:], ".")
	if !found || major == "" || minor == "" {
		return false
	}

	parts := [...]string{major, minor}

	for _, part := range parts {
		for _, digit := range part {
			if digit < '0' || digit > '9' {
				return false
			}
		}
	}

	return true
}

func validateAlpineRelease(release *AlpineRelease) error {
	if !validAlpineBranch(release.Branch) {
		return errors.New("invalid Alpine release branch")
	}

	patch, found := strings.CutPrefix(release.Version, release.Branch[1:]+".")
	if !found || patch == "" {
		return errors.New("alpine release version does not match its branch")
	}

	for _, digit := range patch {
		if digit < '0' || digit > '9' {
			return errors.New("invalid Alpine release version")
		}
	}

	if release.File != "alpine-virt-"+release.Version+"-x86_64.iso" {
		return errors.New("invalid Alpine virtual ISO filename")
	}

	if release.Size < downloadParts || release.Size > maxNetbootSize {
		return fmt.Errorf("invalid Alpine ISO size %d", release.Size)
	}

	digest, err := hex.DecodeString(release.SHA256)
	if err != nil || len(digest) != sha256.Size {
		return errors.New("invalid Alpine ISO SHA-256")
	}

	return nil
}
