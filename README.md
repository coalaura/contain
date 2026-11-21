<picture>
	<source media="(prefers-color-scheme: dark)" srcset=".github/banner.svg">
	<source media="(prefers-color-scheme: light)" srcset=".github/banner-light.svg">
	<img alt="contain: an isolated Linux workspace, illustrated by an agent held inside the square o." src=".github/banner-light.svg">
</picture>

Run tools and agents in an isolated Linux workspace powered by OpenVMM.

`contain` boots a direct-boot Linux VM, exposes the current working directory at `/workspace` over virtio-fs and hands over an interactive terminal once the guest is ready. VM and boot output are captured to a per-session log in `~/.local/contain/` so the host terminal stays clean until startup finishes.

## Requirements

- `openvmm` available on `PATH` with host virtualization enabled
- An Alpine image with Bash as its login shell, as produced by `contain create` or the bundled Alpine builder (see [Guest Image](#guest-image))

## Installation

Download a prebuilt binary from [GitHub Releases](https://github.com/coalaura/contain/releases) and place it on your `PATH`.

Or build from source with Go 1.27+. Generate the embedded Linux guest helper before building (and again after changing its source):

```sh
go generate .
go build -o contain .
```

## Configuration

On first run, `contain` writes a default configuration file to `~/.config/contain.yml`:

```yaml
machine:
  source: ~/.local/contain/images/alpine
  processors: 4
  memory: 4 # GiB (1-32)
  login:
    username: root
    password: alpine
  command: "opencode" # optional; shuts down the VM on exit

mounts:
  - name: downloads
    host: /path/to/downloads
  - name: opencode
    target: config
    host: ~/.config/opencode
    readonly: true
    writable:
      - context-mode
```

- The current working directory is always mounted read/write at `/workspace`.
- Mounts use `target: projects` by default, mounting at `/projects/<name>`. Use `target: config` for the guest's `$HOME/.config/<name>` or `target: local` for `$HOME/.local/<name>`. Names may contain ASCII letters, digits, `.`, `_` and `-` (excluding `.` and `..`).
- Host paths support `~` for the host user's home, including `~\.config\opencode` on Windows. This also works with `--mount` and `machine.source`. Dry-run shows guest home-relative destinations as `~/.config/<name>` or `~/.local/<name>` until the guest home can be resolved at login.
- `writable` exports existing relative subdirectories read/write beneath a `readonly: true` mount, allowing selective persistence without making the whole mount writable.
- When `machine.command` is set, it runs in Bash inside `/workspace` and shuts down the guest when it exits. Leave it empty to start an interactive shell instead. Exiting the interactive shell with `exit` or Ctrl+D also shuts down the guest, including when using `--shell`.
- Bash history is temporarily disabled during session initialization, keeping internal setup and shutdown commands out of history. The previous history setting is restored before handing over the terminal.

## Usage

```sh
contain [run] [options]
contain create [--size 8G] [--tools]
```

`run` is the default subcommand: `contain --shell` is equivalent to `contain run --shell`. The session flags below belong to `run`; `create` has its own flags. Use `contain run --help` or `contain create --help` for command-specific help and `contain --version` for version information.

| Flag | Description |
| --- | --- |
| `--shell` | Start an interactive shell instead of the configured command. |
| `--run COMMAND` | Run a single-line shell command instead of the configured command. Cannot be combined with `--shell`. |
| `--mount HOST[:NAME][:ro\|rw]` | Add a session-only mount under `/projects/<name>`. Repeatable; defaults to read/write and infers `NAME` from the host directory basename. |
| `--temporary`, `--temp` | Discard guest disk changes on exit using an OpenVMM memory-backed diff layer. `disk.raw` stays untouched; writable host mounts still persist. |
| `--dry-run`, `--dry` | Validate config and flags, then print the resolved mounts, disk mode and command without starting the VM. |
| `--help` | Show command-specific help. |

### Examples

```sh
# Start with the configured command (or a shell if none is configured)
contain

# Skip the configured command and open a shell
contain --shell

# Override the startup command for one session
contain --run "pytest"

# Run against a temporary memory-backed disk layer
contain --temp

# Add session-only mounts
contain --mount ./data
contain --mount ./datasets:ro
contain --mount ./shared:docs:ro
contain --mount ./dir:ro:rw # mounts at /projects/ro read/write

# Preview resolved mounts, disk mode and command without booting
contain --dry --temp --mount ./datasets:ro
```

## Guest Image

Create a bootable x86_64 Alpine Linux image with OpenVMM on Windows or Linux:

```sh
contain create             # 8 GiB disk
contain create --size 16G  # custom disk size
contain create --tools     # include development tools and dependencies
```

This downloads the latest stable Alpine netboot kernel and initramfs, formats a raw ext4 disk in Go and installs `alpine-base`, `linux-virt` and Bash inside a RAM-booted VM. Bash is the default login shell, including without `--tools`. The installer downloads Alpine's matching module archive and exports the installed boot files through virtio-fs. Internet access is required on the host and in the guest. No host-side Linux formatting or mounting tools are needed. Disk sizes use binary units and must be at least 512 MiB.

With `--tools`, an extra step inside Alpine installs common packages including Python, C/C++ build tools, Git and shell utilities. It installs the latest [Tap](https://github.com/coalaura/tap), then uses `tap get` for Go, PACE, Zig, Bun's musl build, builder, vet and ffwebp's full build. Their binary directories are added to the guest's login `PATH`.

The completed `disk.raw`, `vmlinuz-virt` and `initramfs-virt` are stored in `~/.local/contain/images/alpine/`, the default `machine.source`, with login `root` / `alpine`. Existing image directories are refused. If you already have a configuration, set its `machine.source` to this directory. Installer output is captured in `~/.local/contain/contain-create-*.log`.

The `alpine/` directory also includes helper scripts for building an image on a Linux host:

```sh
cd alpine
./setup-deps.sh
./build-alpine.sh
```

This produces `disk.raw`, `vmlinuz-virt` and `initramfs-virt` in `alpine/build/` with default login `root` / `alpine`. Point `machine.source` in `~/.config/contain.yml` at that directory.

An optional `./prepare-vm.sh` script is also included to preinstall common development toolchains into a freshly built `disk.raw`.

## Notes

- Console communication uses a per-session named pipe on Windows and a Unix domain socket on Linux and macOS.
- Terminal dimensions are synchronized at startup and on host resize events, using `SIGWINCH` on Unix and console input events on Windows. A bundled Go helper receives updates over COM2 and resizes `/dev/hvc0`; existing images do not need rebuilding.
- Session logs are written to `~/.local/contain/contain-*.log`.
- Empty mountpoint directories under `/projects` and the guest's `$HOME/.config` are removed automatically on unmount and normal guest shutdown; the parent directories are preserved.
