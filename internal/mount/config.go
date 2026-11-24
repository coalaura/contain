package mount

import (
	"fmt"
	"path/filepath"
	"strconv"

	"github.com/coalaura/contain/internal/pathutil"
)

type Config struct {
	Host     string   `yaml:"host"`
	Guest    string   `yaml:"guest"`
	Readonly bool     `yaml:"readonly"`
	Writable []string `yaml:"writable,omitempty"`
}

func Resolve(configured []Config, workspace string, additional ...Config) ([]Mount, error) {
	workspace, err := filepath.Abs(workspace)
	if err != nil {
		return nil, fmt.Errorf("resolve workspace: %w", err)
	}

	basename := filepath.Base(workspace)
	if basename == string(filepath.Separator) {
		basename = "root"
	}

	groups := [2][]Config{configured, additional}
	count := len(configured) + len(additional) + 1

	for _, group := range groups {
		for index := range group {
			count += len(group[index].Writable)
		}
	}

	mounts := make([]Mount, 1, count)

	mounts[0] = Mount{
		Tag:       "workspace",
		GuestPath: "/workspace/" + basename,
		HostPath:  workspace,
	}

	number := 0

	for _, group := range groups {
		for index := range group {
			mount := &group[index]

			guestPath, err := pathutil.Guest(mount.Guest)
			if err != nil {
				return nil, fmt.Errorf("mounts[%d].guest: %w", number, err)
			}

			if len(mount.Writable) > 0 && !mount.Readonly {
				return nil, fmt.Errorf("mounts[%d].writable requires readonly: true", number)
			}

			host, err := pathutil.Host(mount.Host)
			if err != nil {
				return nil, fmt.Errorf("mounts[%d].host: %w", number, err)
			}

			tag := "project" + strconv.Itoa(number)

			mounts = append(mounts, Mount{
				Tag:       tag,
				GuestPath: guestPath,
				HostPath:  host,
				Readonly:  mount.Readonly,
			})

			for overrideIndex, relative := range mount.Writable {
				local, err := localWritablePath(relative)
				if err != nil {
					return nil, fmt.Errorf("mounts[%d].writable[%d]: %w", number, overrideIndex, err)
				}

				mounts = append(mounts, Mount{
					Tag:        tag + "_write" + strconv.Itoa(overrideIndex),
					GuestPath:  guestPath + "/" + relative,
					HostPath:   filepath.Join(host, local),
					ParentPath: guestPath,
				})
			}

			number++
		}
	}

	err = Validate(mounts)
	if err != nil {
		return nil, err
	}

	return mounts, nil
}
