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
    credentials: none # none (or empty) | opencode
    allow_loopback: false # access to the host itself
    allow_lan: false # access to private and directly connected networks
  dns:
    primary: 1.1.1.1:53
    secondary: "[2606:4700:4700::1111]:53"

logs:
  max_size_mib: 10 # per file (1-1024); at most two files per session

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
  allow_loopback: false
  allow_lan: false
  allow:
    - api.openai.com
    - api.anthropic.com
    - 140.82.112.0/20
  deny:
    - blocked.example.com
    - 10.0.0.0/8
    - 169.254.0.0/16
dns: # under machine, alongside network
  primary: 1.1.1.1:53
  secondary: https://dns.google/dns-query
```

- **Modes & rules:** Defaults to `isolated` with `default: deny`. Rules accept IPv4/IPv6 addresses, canonical CIDRs and exact ASCII domain names (use punycode for internationalized names). Domain matching is case-insensitive, ignores a final dot and never expands to subdomains or wildcards: `example.com` does not match `test.example.com`. In `isolated`, `default` applies to both unmatched destination IPs and unmatched DNS names; `deny` overrides `allow` within each rule type. IP-only allow rules do not authorize DNS queries. `open` forwards DNS without domain checks and ignores IP allow/deny lists, while retaining the host/LAN opt-ins below. `closed` blocks all networking, including the host DNS service.
- **Host access:** `allow_loopback: false` blocks loopback addresses and all addresses assigned to the host's interfaces, including its LAN and public addresses. Set it to `true` to permit host destinations, subject to the mode and IP rules. `allow_lan` alone never grants host access.
- **LAN access:** `allow_lan: false` blocks other destinations in private IPv4/IPv6 ranges, IPv4 link-local, shared address space (`100.64.0.0/10`) and directly connected interface subnets (including public and VPN subnets). Set it to `true` to permit these destinations, subject to the mode and IP rules. Host destinations are governed separately by `allow_loopback`. Interface discovery fails closed and is refreshed before socket creation, ICMP sends and each outbound UDP datagram.
- **Host DNS:** The guest's sole configured resolver is the host gateway at `192.0.2.1:53`, supporting UDP and TCP. This service is always allowed except in `closed` mode, even if its IP appears in `deny`. In `isolated`, denied queries receive `REFUSED`; with `default: deny`, only explicitly allowed names are forwarded. `machine.dns.primary` and `.secondary` accept `IP:port`, `domain:port` or an HTTPS DNS-over-HTTPS URL. Bracket IPv6 addresses, include ports for ordinary DNS, and set `secondary: ""` to disable fallback. Ordinary upstream DNS uses UDP with TCP fallback for truncated replies; DoH uses POST with normal TLS verification. The secondary is tried on upstream failure, not on a valid negative answer. Upstream hostnames are bootstrapped through the host's system resolver. These configured host-side upstream connections are exempt from guest network rules, so no DNS-server IP allow rules are needed.
- **DNS-derived IP grants:** Positive responses are cached in a bounded, session-local in-memory LRU. In `isolated`, A/AAAA answers for the queried name, including addresses reached through its CNAME chain, temporarily authorize those destination IPs. Unrelated records and additional-section addresses do not grant access. CNAME targets need no separate allow entry when included in that response, but an explicitly denied CNAME target is refused; a later independent query for an alias is checked normally. IP denies and host/LAN opt-ins still take precedence. Cache entries and their grants expire at the shortest record TTL in the response, or earlier on eviction; cache hits return reduced TTLs and do not extend grants. Zero-TTL responses and negative answers are not cached and create no grants. Expiry or eviction can interrupt existing connections, and guest-side caching can outlive an evicted grant; explicitly allow stable IPs/CIDRs where that behavior is unsuitable.
- **Domain-filtering limitations:** This is DNS-assisted IP filtering, not full domain enforcement. Once an IP is allowed, the guest can contact other domains sharing it, use a known IP directly, or use a proxy/tunnel at that IP. A domain deny rule refuses host-resolver queries; it does not globally block that domain's IPs. The guest can use its own DNS, including external port 53 or DoH, if both the resolver IP and the resulting destination IP pass the normal network rules. External DNS ports are not specially blocked, and those responses do not create host DNS grants. Complete domain enforcement would require in-depth packet inspection and protocol-specific handlers, including handling encrypted protocols; that is outside this implementation's scope and will not be implemented.
- **Routing:** There is no inbound port forwarding. Outbound traffic uses the host's source address and routing, so allowed loopback/private targets can reach host or LAN services; treat any allowed destination (and any proxy/tunnel on it) as a capability.
- **Protocols:** TCP, UDP and ICMP echo are supported; unknown protocols are rejected even in `open` mode. Fragmented packets, IPv4 options, IPv6 extension headers, multicast, unspecified destinations, scoped IPv6 link-local destinations, ICMP errors and path-MTU discovery are unsupported. Linux echo requires unprivileged ping sockets (`ping_group_range`) and fails rather than elevating privileges; Windows uses the native IP Helper echo API.
- **HTTPS interception:** Only `isolated` mode with `credentials: opencode` intercepts TCP port 443, supporting HTTP/1.1 and HTTP/2 with incremental streaming, SSE, cancellation and backpressure. Upstream connections are pinned to the original destination IP with standard TLS verification; guest SNI or `Host` cannot redirect traffic. Invalid identities, mismatched `Host`, `CONNECT`, upgrades and failed TLS verification are rejected. Certificate-pinned clients, incompatible trust stores, ECH and non-HTTP TLS on port 443 are unsupported while interception is enabled.
- **Transparent traffic & QUIC:** With credentials set to `none` (the default), omitted or empty, HTTPS is relayed unchanged. `open` always relays TCP/UDP transparently, including UDP port 443 for HTTP/3/QUIC, even if credentials are configured. Credential loading, injection and guest placeholders are inactive in `open` and `closed` modes. UDP port 443 remains blocked in `isolated`, including without credentials; `closed` blocks all networking. Opaque encrypted payloads cannot be inspected, and destination IP restrictions are not a content filter.
- **Host CA & trust:** The host CA signing key stays in `~/.local/contain/network-ca/` under host permissions. Only the public certificate is installed into Alpine's trust store (and exported via `NODE_EXTRA_CA_CERTS`). Existing images need the required packages/modules or rebuilding.
- **Limits & timeouts:** Sessions are capped at 128 flows, 128 host sockets, 64 concurrent HTTPS requests and 8 HTTP/2 streams per guest connection, with 64 KiB TCP buffers, 16 KiB streaming buffers and 32 KiB header limits. DNS adds a 32-query upstream concurrency cap, a 1024-entry / 8 MiB question-and-response data cache, and at most 64 granted addresses per response. It supports standard single-question IN queries, up to 4 KiB per query and 65535 bytes per response; large UDP replies are truncated for TCP retry. Timeouts are 4s per DNS upstream attempt, 10s other dial/handshake, 20s transport liveness, 30s UDP/DNS idle and 2m TCP/HTTPS idle. Excess traffic is rejected or dropped rather than queued unboundedly.

### Host OpenCode credentials

Set `machine.network.credentials: opencode` with `mode: isolated` to use host API keys (OpenAI, OpenRouter, Anthropic, Groq, Mistral, DeepSeek) or host OAuth logins (OpenAI/ChatGPT, Anthropic) without exposing real secrets to the VM.

- **Credential sources:** Reads host `OPENCODE_AUTH_CONTENT` first (matching OpenCode 1.19.6 auth CLI precedence), then `$XDG_DATA_HOME/opencode/auth.json` or `~/.local/share/opencode/auth.json`. Supports `type: oauth` (`access`, `refresh`, `expires` in Unix milliseconds, optional OpenAI `accountId`). Does not import SQLite integration credentials. Malformed entries, OAuth for other providers, `wellknown` entries and unrecognized providers fail startup.
- **Placeholders & injection:** The guest receives non-secret placeholder entries (with distant OAuth expiry) and, for OAuth, `OPENCODE_CONFIG_CONTENT` with placeholder SDK keys and OpenAI's Codex base URL; normal on-disk config still applies. Real keys, tokens, account IDs and the CA private key never enter the guest or gateway logs. Auth is replaced only for exact approved TLS identities and API paths over independently verified upstream TLS; IP policy still applies, and redirects are never followed with injected auth.
- **API-key scope:** Host API keys authorize only exact `POST` inference paths and `GET` model lists: OpenAI `/v1/chat/completions`, `/v1/responses`, `/v1/responses/compact`, `/v1/models`; OpenRouter `/api/v1/chat/completions`, `/api/v1/responses`, `/api/v1/models`; Anthropic `/v1/messages`, `/v1/messages/count_tokens`, `/v1/models`; Groq `/openai/v1/chat/completions`, `/openai/v1/models`; Mistral `/v1/chat/completions`, `/v1/fim/completions`, `/v1/models`; DeepSeek `/chat/completions`, `/models` and their `/v1` equivalents. Other operations on a provider with a loaded host key receive HTTP 403, including file access, stored-response retrieval, batch jobs and account management. Encoded path aliases are rejected. All guest processes can use the permitted operations, so inference usage still consumes the host account's quota.
- **OpenAI OAuth:** Authorizes `POST https://chatgpt.com/backend-api/codex/responses` and `/responses/compact` with host-managed `ChatGPT-Account-Id` when available. It is not an `api.openai.com` API key; use a Responses/Codex-compatible model.
- **Anthropic OAuth:** Authorizes `POST https://api.anthropic.com/v1/messages` and `/v1/messages/count_tokens`, replacing `X-Api-Key` with bearer auth and adding `anthropic-beta: oauth-2025-04-20`. Provider scopes, subscription eligibility and client restrictions still apply; `contain` does not impersonate Claude Code, rewrite prompts/tool names, or translate incompatible protocols.
- **Host-only refresh:** Runs on the host shortly before expiry against the fixed OpenAI or Anthropic token endpoint with normal TLS verification and no redirects. Refresh uses the same host DNS resolver, domain rules, cache and temporary IP grants as guest queries, and resolved IPs must pass destination policy. With `default: deny`, allow the inference domains plus `auth.openai.com` for OpenAI or `console.anthropic.com` for Anthropic; no upstream DNS-server IP rules are needed. Failed refreshes fail the request with a short retry cooldown; proxied requests are never buffered or replayed.
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
- Session logs are written to `~/.local/contain/contain-*.log`. Each session keeps at most two files: the current `.log` and the previous `.1.log`. When the current file is full, the previous file is deleted, the current file becomes the previous file, and a new current file is opened. `logs.max_size_mib` limits each file (default 10 MiB, range 1-1024), including console output and OpenVMM stdout/stderr. Installer logs use the same rotation with a fixed 10 MiB per-file limit because `contain create` does not load the VM configuration. Completed sessions' logs remain on disk; the two-file limit is per session.
- Empty mountpoint directories are removed automatically on unmount and normal guest shutdown; their parent directories are preserved.
