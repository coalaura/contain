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

Normal sessions have no Ethernet NIC. A bundled guest helper creates a 1280-byte TUN interface and forwards framed IPv4/IPv6 packets over virtio-vsock to a Go userspace gateway on the host. Networking is ready before the user's command starts, applies to `--plain` and `--temporary`, and fails closed if the helper or transport stops.

The guest helper is untrusted: the host independently validates packet lengths, source addresses, checksums, protocols and destinations before forwarding, and enforces destination policy again when opening host sockets.

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

- **Modes & rules:** Defaults to `isolated` with `default: deny` (no external access). `open` permits supported outbound traffic without IP filtering; `closed` blocks all external networking. In `isolated`, explicit `deny` always wins (`default: deny` requires an `allow` match; `default: allow` permits anything not denied). Rules accept IPv4/IPv6 addresses or canonical CIDRs only, not domains.
- **DNS & routing:** DNS uses `1.1.1.1` and `2606:4700:4700::1111` under the same IP rules, and allowing DNS does not authorize resolved addresses. There is no inbound port forwarding. Outbound traffic uses the host's source address and routing, so allowed loopback/private targets can reach host or LAN services; treat any allowed destination (and any proxy/tunnel on it) as a capability.
- **Protocols:** TCP, UDP and ICMP echo are supported; unknown protocols are rejected even in `open` mode. Fragmented packets, IPv4 options, IPv6 extension headers, multicast, unspecified destinations, scoped IPv6 link-local destinations, ICMP errors and path-MTU discovery are unsupported. Linux echo requires unprivileged ping sockets (`ping_group_range`) and fails rather than elevating privileges; Windows uses the native IP Helper echo API.
- **HTTPS interception:** All TCP port 443 traffic passes through transparent HTTPS interception (HTTP/1.1 and HTTP/2) with incremental streaming, SSE, cancellation and backpressure. Upstream connections are pinned to the packet's original destination IP with standard TLS verification for the TLS identity; guest SNI or `Host` cannot redirect traffic. Invalid identities, mismatched `Host`, `CONNECT`, upgrades and failed TLS verification are rejected. UDP port 443 is blocked to prevent QUIC bypass. HTTP/3, certificate-pinned clients, incompatible trust stores, ECH and non-HTTP TLS on port 443 are unsupported. Other ports carry opaque TCP/UDP: arbitrary encrypted payloads cannot be inspected, and IP filtering is not a content filter.
- **Host CA & trust:** The host CA signing key stays in `~/.local/contain/network-ca/` under host permissions. Only the public certificate is installed into Alpine's trust store (and exported via `NODE_EXTRA_CA_CERTS`). Existing images need the required packages/modules or rebuilding.
- **Limits & timeouts:** Sessions are capped at 128 flows, 128 host sockets, 64 concurrent HTTPS requests and 8 HTTP/2 streams per guest connection, with 64 KiB TCP buffers, 16 KiB streaming buffers and 32 KiB header limits. Timeouts are 10s dial/handshake, 20s transport liveness, 30s UDP idle and 2m TCP/HTTPS idle. Excess traffic is rejected or dropped rather than queued unboundedly.

### Host OpenCode credentials

Set `machine.network.credentials: opencode` to use host API keys (OpenAI, OpenRouter, Anthropic, Groq, Mistral, DeepSeek) or host OAuth logins (OpenAI/ChatGPT, Anthropic) without exposing real secrets to the VM.

- **Credential sources:** Reads host `OPENCODE_AUTH_CONTENT` first (matching OpenCode 1.19.6 auth CLI precedence), then `$XDG_DATA_HOME/opencode/auth.json` or `~/.local/share/opencode/auth.json`. Supports `type: oauth` (`access`, `refresh`, `expires` in Unix milliseconds, optional OpenAI `accountId`). Does not import SQLite integration credentials. Malformed entries, OAuth for other providers, `wellknown` entries and unrecognized providers fail startup.
- **Placeholders & injection:** The guest receives non-secret placeholder entries (with distant OAuth expiry) and, for OAuth, `OPENCODE_CONFIG_CONTENT` with placeholder SDK keys and OpenAI's Codex base URL; normal on-disk config still applies. Real keys, tokens, account IDs and the CA private key never enter the guest or gateway logs. Auth is replaced only for exact approved TLS identities and API paths over independently verified upstream TLS; IP policy still applies, and redirects are never followed with injected auth.
- **OpenAI OAuth:** Authorizes `POST https://chatgpt.com/backend-api/codex/responses` and `/responses/compact` with host-managed `ChatGPT-Account-Id` when available. It is not an `api.openai.com` API key; use a Responses/Codex-compatible model.
- **Anthropic OAuth:** Authorizes `POST https://api.anthropic.com/v1/messages` and `/v1/messages/count_tokens`, replacing `X-Api-Key` with bearer auth and adding `anthropic-beta: oauth-2025-04-20`. Provider scopes, subscription eligibility and client restrictions still apply; `contain` does not impersonate Claude Code, rewrite prompts/tool names, or translate incompatible protocols.
- **Host-only refresh:** Runs on the host shortly before expiry against the fixed OpenAI or Anthropic token endpoint with normal TLS verification and no redirects. Refresh DNS uses policy-checked TCP DNS to `1.1.1.1` / `2606:4700:4700::1111`, and resolved IPs must also pass policy (isolated allow-lists must permit DNS, inference IPs and `auth.openai.com` / `console.anthropic.com` IPs). Failed refreshes fail the request with a short retry cooldown; proxied requests are never buffered or replayed.
- **Rotation storage & locking:** Rotated tokens are journaled in owner-only `~/.local/contain/network-auth` before atomic writeback to file-backed OpenCode logins (preserving unknown fields and other providers). For env-supplied credentials, the journal retains rotations across restarts for the same original value without updating the parent environment. A host file lock serializes `contain` refreshes for the same data directory, but host OpenCode does not take that lock: avoid concurrent refreshes between host OpenCode and `contain`, restart `contain` after changing accounts, and keep the rotation store private and persistent.
- **Isolation boundary:** Do not mount or snapshot host credentials into the guest; known OpenCode credential, rotation-store and CA paths (including containing directories) are rejected. This is not a general secret scanner: remove credentials from old guest disks and audit other shared files. Same-user host processes and approved providers remain trusted.

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

This downloads the latest stable Alpine netboot kernel and initramfs, formats a raw ext4 disk in Go and installs `alpine-base`, `linux-virt`, Bash, `ca-certificates` and `iproute2` inside a RAM-booted VM. Bash is the default login shell, including without `--tools`. No host-side Linux formatting or mounting tools are needed; disk sizes use binary units and must be at least 512 MiB.

The installer downloads Alpine's matching module archive and exports the installed boot files through virtio-fs. Internet access is required on the host and in the guest: provisioning deliberately retains unrestricted Consomme networking to install Alpine before the normal-session gateway exists (`machine.network` applies to normal sessions, not `contain create`).

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
