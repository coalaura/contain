<picture>
	<source media="(prefers-color-scheme: dark)" srcset=".github/banner.svg">
	<source media="(prefers-color-scheme: light)" srcset=".github/banner-light.svg">
	<img alt="contain: an isolated Linux workspace, illustrated by an agent held inside the square o." src=".github/banner-light.svg">
</picture>

Run tools and agents in an isolated Linux workspace powered by OpenVMM.

`contain` boots a direct-boot Linux VM, exposes the current working directory at `/workspace` over virtio-fs and hands over an interactive terminal once the guest is ready. VM and boot output are captured to a per-session log in `~/.local/contain/` so the host terminal stays clean until startup finishes.

## Requirements

- `openvmm` available on `PATH` with host virtualization enabled
- An Alpine image with Bash as its login shell, `ca-certificates`, an IPv6-capable `ip` command (BusyBox or `iproute2`), and the TUN and virtio-vsock kernel modules, as produced by `contain create` or the bundled Alpine builder (see [Guest Image](#guest-image))

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
  network:
    mode: isolated # open | isolated | closed
    default: deny # allow | deny
    allow: []
    deny: []
    credentials: "" # optional: opencode

mounts:
  - host: /path/to/downloads
    guest: /projects/downloads
  - host: ~/.config/opencode
    guest: ~/.config/opencode
    readonly: true
    writable:
      - context-mode

snapshots:
  - host: /path/to/preferences.json
    guest: ~/.config/example/preferences.json
```

- The current working directory is always mounted read/write at `/workspace`.
- Mounts require an explicit `guest` directory, one level below `/projects`, `~/.config`, `~/.local`, `~/.local/share`, `~/.local/state` or `~/.local/bin`. For example, `guest: ~/.local/share/opencode` mounts directly at `/root/.local/share/opencode`. Replace older `name` + `target` entries with the full `guest` path.
- Host paths support `~` for the host user's home, including `~\.config\opencode` on Windows. This also works with `--mount` and `machine.source`. Guest `~` means `/root`, the home in the supported Alpine root session; absolute equivalents work too. Dry-run shows the resolved absolute guest paths.
- `writable` exports existing relative subdirectories read/write beneath a `readonly: true` mount, allowing selective persistence without making the whole mount writable.
- `snapshots` copies individual host files into the guest once per session, before the shell or command starts. `guest` accepts any absolute guest file path or `~/...`. Missing parent directories are created and existing files are replaced with private (`0600`) copies. Empty files are supported. The guest can modify its copies, but changes are never synchronized with the source files. Copies remain on persistent guest disks until replaced at the next startup; `--temporary` discards them with the rest of the guest disk changes.
- Go validates host sources, guest path syntax and configured overlaps before starting the VM, including during dry-run. Snapshot paths cannot overlap host mounts or other snapshots; mount paths cannot overlap except for explicit writable overrides. Paths must be canonical, without traversal, trailing slashes or control characters. Validation uses configured paths, not the guest filesystem: it does not resolve guest symlinks or inspect existing mounts. Parent directories in the guest must not redirect configured paths into host mounts.
- When `machine.command` is set, it runs in Bash inside `/workspace` and shuts down the guest when it exits. Leave it empty to start an interactive shell instead. Exiting the interactive shell with `exit` or Ctrl+D also shuts down the guest, including when using `--shell`.
- Bash history is temporarily disabled during session initialization, keeping internal setup and shutdown commands out of history. The previous history setting is restored before handing over the terminal.

### Networking

Normal sessions have no Ethernet NIC. A bundled guest helper creates a 1280-byte TUN interface and carries framed IPv4/IPv6 packets over virtio-vsock to a Go userspace gateway on the host. Networking is ready before the user's command starts. Policy also applies to `--plain` and `--temporary`; stopping the helper or losing the transport fails closed. Guest root can change routes or send its own packets, but the host independently validates packet lengths, source addresses, checksums, protocols and destinations before forwarding, and checks destination policy again when opening host sockets.

The default is `isolated` with `default: deny`, so an unconfigured session has no external access. `open` permits supported outbound traffic without IP filtering; `closed` blocks all external networking. In `isolated`, explicit `deny` always wins: `default: deny` requires a matching `allow`, while `default: allow` permits destinations unless denied. Rules accept individual IPv4/IPv6 addresses or canonical CIDRs, not domains. For example:

```yaml
network: # under machine
  mode: isolated
  default: deny
  allow:
    - 1.1.1.1
    - 2606:4700:4700::1111
    - 140.82.112.0/20
  deny:
    - 10.0.0.0/8
    - 169.254.0.0/16
```

DNS uses `1.1.1.1` and `2606:4700:4700::1111`, subject to the same rules as other traffic. Allowing DNS does not authorize the addresses returned by DNS. There is no inbound port forwarding. Outbound traffic uses the host's source address and routing; allowed loopback and private destinations can reach host or LAN services. Treat an allowed destination as a capability, including any proxy or tunnel it offers.

TCP, UDP and ICMP echo are supported. Unknown protocols are rejected even in `open` mode. Fragmented packets, IPv4 options, IPv6 extension headers, multicast, unspecified destinations and scoped IPv6 link-local destinations are unsupported; ICMP errors and path-MTU discovery are not relayed. Linux echo requires the OS to permit unprivileged ping sockets (`ping_group_range`); if disabled, echo fails rather than acquiring privileges. Windows uses the native IP Helper echo API.

All TCP port 443 traffic must pass HTTPS interception, supporting HTTP/1.1 and HTTP/2 with incremental request/response streaming, SSE, cancellation and backpressure. The upstream connection is pinned to the packet's original destination IP and uses normal certificate verification for the TLS identity; guest SNI or Host cannot select another destination. Invalid identities, mismatched Host, CONNECT, upgrades and failed TLS verification are rejected. UDP port 443 is blocked to prevent QUIC bypass. HTTP/3, certificate-pinned clients, incompatible trust stores, ECH and non-HTTP TLS on port 443 are unsupported. Other ports carry opaque TCP/UDP: arbitrary encrypted payloads or tunnels cannot be inspected, and IP filtering is not a general content filter.

The host CA signing key stays in `~/.local/contain/network-ca/`, protected by host permissions. Only the public certificate is installed into Alpine's trust store during provisioning and session setup; Node-compatible clients also receive `NODE_EXTRA_CA_CERTS`. Existing images need the packages/modules listed above or rebuilding. The gateway bounds sessions to 128 flows, 128 host sockets and 64 concurrent HTTPS requests, with eight HTTP/2 streams per guest connection, 64 KiB TCP buffers, 16 KiB streaming buffers and 32 KiB HTTP header limits. Dial/handshake timeouts are 10 seconds, transport liveness is 20 seconds, UDP idle timeout is 30 seconds and TCP/HTTPS idle timeout is two minutes. Limits reject or drop excess traffic rather than queueing unbounded work.

### Host OpenCode credentials

Set `machine.network.credentials: opencode` to use host API keys for OpenAI, OpenRouter, Anthropic, Groq, Mistral and DeepSeek. The adapter reads host `OPENCODE_AUTH_CONTENT` first, matching current OpenCode's precedence, then `$XDG_DATA_HOME/opencode/auth.json` or `~/.local/share/opencode/auth.json`. Malformed credentials fail startup. OAuth, refresh-token and `wellknown` credentials, and unrecognized providers, are explicitly unsupported and fail startup rather than being used as API keys.

OpenCode in the guest receives provider entries with non-secret placeholder keys so it can initialize its clients. The gateway replaces authentication only for exact approved provider TLS identities and API paths, after an independently verified upstream TLS connection; normal IP policy still applies. It does not follow redirects itself or carry injected authentication to arbitrary redirect targets. Real keys, refresh tokens and the CA signing key are never part of the helper export or guest credential configuration. Gateway logging does not record authentication or request/response bodies.

Do not snapshot or mount host credentials into the guest. Known OpenCode credential and CA paths are rejected as mount/snapshot sources, including containing directories. This is not a general secret scanner: remove credentials already saved in old guest disks and review other shared files, configuration and tools for embedded secrets. Local host processes running as the same user remain trusted, as do approved providers receiving their own credentials.

## Usage

```sh
contain [run] [options]
contain create [--size 8G] [--tools]
```

`run` is the default subcommand: `contain --shell` is equivalent to `contain run --shell`. The session flags below belong to `run`; `create` has its own flags. Use `contain run --help` or `contain create --help` for command-specific help and `contain --version` for version information.

| Flag | Description |
| --- | --- |
| `--shell` | Start an interactive shell instead of the configured command. |
| `--plain` | Start an interactive shell at `/`, skipping all workspace, configured and CLI mounts, snapshots, and startup commands. Takes precedence over `--shell`, `--run` and `--mount`; can be combined with `--temporary` and `--dry-run`. |
| `--run COMMAND` | Run a single-line shell command instead of the configured command. Cannot be combined with `--shell`. |
| `--mount HOST[:NAME][:ro\|rw]` | Add a session-only mount under `/projects/<name>`. Repeatable; defaults to read/write and infers `NAME` from the host directory basename. |
| `--temporary`, `--temp` | Discard guest disk changes on exit using an OpenVMM memory-backed diff layer. `disk.raw` stays untouched; writable host mounts still persist. |
| `--dry-run`, `--dry` | Validate config and flags, then print the resolved mounts, whether they hide existing guest files, snapshots, disk mode and command without starting the VM. |
| `--help` | Show command-specific help. |

Dry-run inspects `disk.raw` read-only. `OVERRIDES FILES` is `yes` when a mount destination contains any file, including an empty file, anywhere below it; trees containing only empty directories count as empty. Symlinks and other non-directory entries count as content. Writable overrides show `n/a (parent mount)` because they cover a host export rather than the guest disk. Unreadable destinations or symlinks in the destination path show `unknown` with a reason. This is an offline preview of the disk, not a check of paths created or changed during boot, and does not change mounting behavior.

### Examples

```sh
# Start with the configured command (or a shell if none is configured)
contain

# Skip the configured command and open a shell
contain --shell

# Open a plain shell at / without host mounts or snapshots
contain --plain

# Override the startup command for one session
contain --run "pytest"

# Run against a temporary memory-backed disk layer
contain --temp

# Add session-only mounts
contain --mount ./data
contain --mount ./datasets:ro
contain --mount ./shared:docs:ro
contain --mount ./dir:ro:rw # mounts at /projects/ro read/write

# Preview resolved mounts, snapshots, disk mode and command without booting
contain --dry --temp --mount ./datasets:ro
```

## Guest Image

Create a bootable x86_64 Alpine Linux image with OpenVMM on Windows or Linux:

```sh
contain create             # 8 GiB disk
contain create --size 16G  # custom disk size
contain create --tools     # include development tools and dependencies
```

This downloads the latest stable Alpine netboot kernel and initramfs, formats a raw ext4 disk in Go and installs `alpine-base`, `linux-virt`, Bash, `ca-certificates` and `iproute2` inside a RAM-booted VM. Bash is the default login shell, including without `--tools`. The installer downloads Alpine's matching module archive and exports the installed boot files through virtio-fs. Internet access is required on the host and in the guest. Provisioning deliberately retains unrestricted Consomme networking to install Alpine before the normal-session gateway exists; `machine.network` applies to normal sessions, not `contain create`. No host-side Linux formatting or mounting tools are needed. Disk sizes use binary units and must be at least 512 MiB.

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
- Empty mountpoint directories are removed automatically on unmount and normal guest shutdown; their parent directories are preserved.
