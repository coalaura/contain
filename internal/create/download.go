package create

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"

	"github.com/goccy/go-yaml"
)

const (
	AlpineMirror   = "https://dl-cdn.alpinelinux.org/alpine"
	maxNetbootSize = 256 << 20
)

type alpineRelease struct {
	Branch string `yaml:"branch"`
	Arch   string `yaml:"arch"`
	Flavor string `yaml:"flavor"`
}

func AlpineBranch(ctx context.Context, client *http.Client) (string, error) {
	url := AlpineMirror + "/latest-stable/releases/x86_64/latest-releases.yaml"

	response, err := requestAlpine(ctx, client, url)
	if err != nil {
		return "", err
	}

	defer response.Body.Close()

	data, err := io.ReadAll(io.LimitReader(response.Body, (1<<20)+1))
	if err != nil {
		return "", err
	}

	if len(data) > 1<<20 {
		return "", errors.New("release metadata exceeds 1 MiB")
	}

	var releases []alpineRelease

	err = yaml.Unmarshal(data, &releases)
	if err != nil {
		return "", fmt.Errorf("decode Alpine releases: %w", err)
	}

	for _, release := range releases {
		if release.Arch == "x86_64" && release.Flavor == "alpine-netboot" && validAlpineBranch(release.Branch) {
			return release.Branch, nil
		}
	}

	return "", errors.New("release metadata contains no stable x86_64 Alpine netboot release")
}

func DownloadAlpine(ctx context.Context, client *http.Client, url, destination string) error {
	response, err := requestAlpine(ctx, client, url)
	if err != nil {
		return err
	}

	defer response.Body.Close()

	if response.ContentLength > maxNetbootSize {
		return fmt.Errorf("download %s exceeds 256 MiB", url)
	}

	output, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}

	defer output.Close()

	written, err := io.Copy(output, io.LimitReader(response.Body, maxNetbootSize+1))
	if err != nil {
		return fmt.Errorf("download %s: %w", url, err)
	}

	if written == 0 || written > maxNetbootSize {
		return fmt.Errorf("download %s has invalid size %d", url, written)
	}

	return output.Close()
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
