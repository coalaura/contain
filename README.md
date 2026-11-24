<picture>
	<source media="(prefers-color-scheme: dark)" srcset=".github/banner.svg">
	<source media="(prefers-color-scheme: light)" srcset=".github/banner-light.svg">
	<img alt="contain: an isolated Linux workspace, illustrated by an agent held inside the square o." src=".github/banner-light.svg">
</picture>

Run tools and agents in an isolated Linux workspace powered by OpenVMM.

`contain` boots a direct-boot Linux VM, exposes the current working directory at `/workspace` over virtio-fs and hands over an interactive terminal once the guest is ready. VM and boot output are captured to a per-session log in `~/.local/contain/logs/` so the host terminal stays clean until startup finishes.

## Requirements

- [`openvmm`](https://github.com/microsoft/openvmm/actions/workflows/openvmm-ci.yaml?query=is%3Asuccess+branch%3Amain) available on `PATH` with host virtualization enabled
- An Alpine image with Bash as its login shell, `ca-certificates`, an IPv6-capable `ip` command (BusyBox or `iproute2`) and the TUN and virtio-vsock kernel modules, as produced by `contain create` or the bundled Alpine builder (see [Guest Image](#guest-image))

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
  max_size_mib: 10 # per file (1-1024); current + previous for each log

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

When `machine.command` is set, it runs in Bash inside `/workspace` and shuts down the guest when it exits. Leave it empty to start an interactive shell instead; exiting with `exit` or Ctrl+D also shuts down the guest, including when using `--shell`. Bash history is temporarily disabled during initialization so internal setup and shutdown commands stay out of history, then restored before handing over the terminal.

### Mounts & snapshots

The current working directory is always mounted read/write at `/workspace`. Additional mounts require an explicit `guest` directory one level below `/projects`, `~/.config`, `~/.local`, `~/.local/share`, `~/.local/state` or `~/.local/bin` (for example, `guest: ~/.local/share/opencode` mounts directly at `/root/.local/share/opencode`). Replace older `name` + `target` entries with the full `guest` path. Setting `writable` on a `readonly: true` mount exports existing relative subdirectories read/write without making the whole mount writable.

Host paths expand `~` to the host user's home (including `~\.config\opencode` on Windows), including in `--mount`. Guest `~` expands to `/root`, the home in the supported Alpine root session; absolute equivalents work too and dry-run prints the resolved absolute guest paths.

`snapshots` copies individual host files (including empty files) into the guest once per session before the shell or command starts. `guest` accepts any absolute file path or `~/...`; missing parent directories are created and existing files are replaced with private (`0600`) copies. Guest edits are never synced back to the host. Copies remain on persistent guest disks until replaced at the next startup or are discarded by `--temporary`.

Host sources, guest path syntax and configured overlaps are validated before starting the VM, including during dry-run. Paths must be canonical (no traversal, trailing slashes or control characters), snapshots cannot overlap mounts or other snapshots and mounts cannot overlap except for explicit `writable` overrides. Validation checks configured paths only and does not resolve guest symlinks or inspect existing mounts, so guest parent directories must not redirect configured paths into host mounts.

### Networking

Normal sessions have no Ethernet NIC. A bundled guest helper creates a 1280-byte TUN interface and forwards framed IPv4/IPv6 packets over virtio-vsock to a Go userspace gateway on the host. Networking is ready before the user's command starts, applies to `--plain` and `--temporary` and fails closed if the helper or transport stops.

The guest helper is untrusted: the host independently validates packet lengths, source addresses, checksums, protocols and destinations before forwarding and enforces destination policy again when opening host sockets.

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

#### Modes & access rules

| Mode | DNS | IP rules | Host / LAN opt-ins |
| --- | --- | --- | --- |
| `isolated` (default) | Filtered by domain rules (`default` applies to unmatched names) | Filtered by IP/CIDR rules and DNS grants (`default` applies to unmatched IPs) | Enforced |
| `open` | Forwarded without domain checks | Ignored (selective HTTPS interception when credentials are enabled; UDP 443 allowed) | Enforced |
| `closed` | Blocked (including host DNS) | All networking blocked | All networking blocked |

Sessions default to `isolated` mode with `default: deny`. Rules accept IPv4/IPv6 addresses, canonical CIDRs and exact ASCII domain names (use punycode for internationalized names). Domain matching is case-insensitive, ignores a final dot and never expands to subdomains or wildcards (`example.com` does not match `test.example.com`). Within each rule type, `deny` overrides `allow` and IP-only allow rules do not authorize DNS queries.

- `allow_loopback: false` blocks loopback addresses and all addresses assigned to the host's interfaces (including LAN and public addresses). Set it to `true` to permit host destinations, subject to the mode and IP rules. `allow_lan` alone never grants host access.
- `allow_lan: false` blocks other destinations in private IPv4/IPv6 ranges, IPv4 link-local, shared address space (`100.64.0.0/10`) and directly connected interface subnets (including public and VPN subnets). Set it to `true` to permit them, subject to the mode and IP rules. Interface discovery fails closed and refreshes before socket creation, ICMP sends and each outbound UDP datagram.

There is no inbound port forwarding. Outbound traffic uses the host's source address and routing, so allowed loopback or private targets can reach host or LAN services; treat any allowed destination (and any proxy/tunnel on it) as a capability.

Only TCP, UDP and ICMP echo are supported; unknown protocols are rejected even in `open` mode. Fragmented packets, IPv4 options, IPv6 extension headers, multicast, unspecified destinations, scoped IPv6 link-local destinations, ICMP errors and path-MTU discovery are unsupported. Linux echo requires unprivileged ping sockets (`ping_group_range`) and fails rather than elevating privileges; Windows uses the native IP Helper echo API.

#### DNS & IP grants

The guest's sole configured resolver is the host gateway at `192.0.2.1:53` (UDP and TCP), which is always allowed except in `closed` mode even if its IP appears in `deny`. In `isolated`, denied queries receive `REFUSED` and with `default: deny` only explicitly allowed names are forwarded.

`machine.dns.primary` and `.secondary` accept `IP:port`, `domain:port` or an HTTPS DNS-over-HTTPS URL. Bracket IPv6 addresses, include ports for ordinary DNS and set `secondary: ""` to disable fallback. Ordinary DNS uses UDP with TCP fallback for truncated replies; DoH uses POST with normal TLS verification. The secondary is tried on upstream failure, not on a valid negative answer. Upstream hostnames resolve through the host's system resolver and these host-side connections are exempt from guest network rules so no DNS-server IP allow rules are needed.

Positive responses are cached in a bounded, session-local in-memory LRU. In `isolated`, A/AAAA answers for the queried name (including addresses reached through its CNAME chain) temporarily authorize those destination IPs; unrelated and additional-section records do not. CNAME targets need no separate allow entry when included in that response, though an explicitly denied CNAME target is refused and later direct queries for an alias are checked normally. IP denies and host/LAN opt-ins still take precedence.

Cache entries and grants expire at the shortest record TTL in the response or earlier on eviction; cache hits return reduced TTLs and do not extend grants. Zero-TTL and negative answers are not cached and create no grants. Expiry or eviction can interrupt existing connections and guest-side caching can outlive an evicted grant; explicitly allow stable IPs/CIDRs where needed.

This is DNS-assisted IP filtering, not full domain enforcement. Once an IP is allowed, the guest can contact other domains sharing it, use a known IP directly or use a proxy/tunnel at that IP and a domain deny rule refuses host-resolver queries rather than globally blocking that domain's IPs. External DNS (port 53 or DoH) works if both the resolver IP and resulting destination IP pass normal network rules, without creating host DNS grants. Deep packet inspection and protocol-specific encrypted-traffic handlers are outside this implementation's scope and will not be implemented.

#### HTTPS interception & CA

Both `isolated` and `open` modes with `credentials: opencode` inspect TLS ClientHello SNI on TCP port 443 and intercept domains covered by loaded credentials (including `chatgpt.com` for OpenAI OAuth). Other domains and connections without visible SNI are relayed unchanged, preserving end-to-end TLS; encrypted SNI cannot select a hidden domain for credential injection. ClientHello inspection is bounded to 64 KiB and 10 seconds; malformed or oversized hellos are rejected. Intercepted connections support HTTP/1.1 and HTTP/2 with incremental streaming, SSE, cancellation and backpressure. Upstream connections remain pinned to the original destination IP with standard TLS verification so guest SNI or `Host` cannot redirect traffic; invalid identities, mismatched `Host`, `CONNECT`, upgrades and failed TLS verification are rejected. Certificate-pinned clients, incompatible trust stores, ECH and non-HTTP TLS remain unsupported on intercepted domains.

With `credentials` set to `none` (default), omitted or empty, HTTPS is relayed unchanged in `isolated` and `open`. `closed` disables all networking, credential loading, injection and guest placeholders. UDP port 443 remains blocked in `isolated`, including without credentials; `open` relays it transparently for HTTP/3/QUIC, without credential injection. Opaque encrypted payloads cannot be inspected and destination IP restrictions are not a content filter.

Each image has its own CA at `~/.local/contain/images/<name>/network/ca/authority.pem`, alongside its disk and boot files. The signing key stays under host permissions; only the public certificate is installed into Alpine's trust store and exported via `NODE_EXTRA_CA_CERTS`. The CA is created when needed and the guest trust store updates automatically at startup.

#### Limits & timeouts

| Category | Limits |
| --- | --- |
| Concurrency | 128 flows, 128 host sockets, 64 concurrent HTTPS requests, 8 HTTP/2 streams per guest connection |
| Buffers & headers | 64 KiB TCP buffers, 16 KiB streaming buffers, 32 KiB header limit |
| DNS | 128 concurrent UDP requests outside the flow budget, 32 distinct concurrent upstream queries (identical misses share a lookup), 1024-entry / 8 MiB cache, 64 granted addresses per response; standard single-question `IN` queries up to 4 KiB, responses up to 65535 bytes (large UDP replies truncated for TCP retry) |
| Timeouts | 4s per DNS upstream attempt, 10s other dial/handshake, 20s transport liveness, 30s forwarded UDP / DNS-over-TCP idle, 2m TCP/HTTPS idle |

Excess traffic is rejected or dropped rather than queued unboundedly.

### Host OpenCode credentials

Set `machine.network.credentials: opencode` with `mode: isolated` or `mode: open` to use host API keys (OpenAI, OpenRouter, Anthropic, Groq, Mistral, DeepSeek) or host OAuth logins (OpenAI/ChatGPT, Anthropic) without exposing real secrets to the VM.

`contain` reads host `OPENCODE_AUTH_CONTENT` first (matching OpenCode 1.19.6 auth CLI precedence), then `$XDG_DATA_HOME/opencode/auth.json` or `~/.local/share/opencode/auth.json`. It supports `type: oauth` (`access`, `refresh`, `expires` in Unix milliseconds, optional OpenAI `accountId`) and does not import SQLite integration credentials. Malformed entries, OAuth for other providers, `wellknown` entries and unrecognized providers fail startup.

The guest receives non-secret placeholder entries (with distant OAuth expiry) and, for OAuth, `OPENCODE_CONFIG_CONTENT` with placeholder SDK keys and OpenAI's Codex base URL; normal on-disk config still applies. Real keys, tokens, account IDs and the CA private key never enter the guest or gateway logs. Auth is replaced only for exact approved TLS identities and API paths over independently verified upstream TLS; IP policy still applies and redirects are never followed with injected auth.

#### Allowed endpoints

| Provider | Auth type | Permitted paths | Notes |
| --- | --- | --- | --- |
| OpenAI | API key | `POST /v1/chat/completions`, `/v1/responses`, `/v1/responses/compact`; `GET /v1/models` | |
| OpenRouter | API key | `POST /api/v1/chat/completions`, `/api/v1/responses`; `GET /api/v1/models` | |
| Anthropic | API key | `POST /v1/messages`, `/v1/messages/count_tokens`; `GET /v1/models` | |
| Groq | API key | `POST /openai/v1/chat/completions`; `GET /openai/v1/models` | |
| Mistral | API key | `POST /v1/chat/completions`, `/v1/fim/completions`; `GET /v1/models` | |
| DeepSeek | API key | `POST /chat/completions`, `/v1/chat/completions`; `GET /models`, `/v1/models` | |
| OpenAI (`chatgpt.com`) | OAuth | `POST /backend-api/codex/responses`, `/backend-api/codex/responses/compact` | Adds host-managed `ChatGPT-Account-Id` when available; not an `api.openai.com` key (use a Responses/Codex-compatible model) |
| Anthropic (`api.anthropic.com`) | OAuth | `POST /v1/messages`, `/v1/messages/count_tokens` | Replaces `X-Api-Key` with bearer auth and adds `anthropic-beta: oauth-2025-04-20` |

Other operations on a provider with a loaded host key receive HTTP 403 (including file access, stored-response retrieval, batch jobs and account management) and encoded path aliases are rejected. All guest processes can use the permitted operations, so inference still consumes the host account's quota. For Anthropic OAuth, provider scopes, subscription eligibility and client restrictions still apply; `contain` does not impersonate Claude Code, rewrite prompts/tool names or translate incompatible protocols.

#### OAuth refresh & isolation

OAuth refresh runs on the host shortly before expiry against the fixed OpenAI or Anthropic token endpoint with normal TLS verification and no redirects. Refresh uses the same host DNS resolver, domain rules, cache and temporary IP grants as guest queries and resolved IPs must pass destination policy. In `isolated` mode with `default: deny`, allow the inference domains plus `auth.openai.com` (OpenAI) or `console.anthropic.com` (Anthropic); no upstream DNS-server IP rules are needed. Failed refreshes fail the request with a short retry cooldown; proxied requests are never buffered or replayed.

Rotated tokens are journaled in owner-only `~/.local/contain/network/auth` before atomic writeback to file-backed OpenCode logins (preserving unknown fields and other providers). This host-account state is shared across images; for env-supplied credentials, rotations are retained across restarts for the same original value without updating the parent environment.

A persistent `lock` file provides a stable OS-level lock while `tokens.json` is atomically replaced (its presence does not mean a session is running and the OS releases it when its handle closes). Because host OpenCode does not take this lock, avoid concurrent refreshes between host OpenCode and `contain`, restart `contain` after changing accounts and keep the rotation store private and persistent.

Do not mount or snapshot host credentials into the guest: known OpenCode credential, rotation-store and CA paths (including containing directories) are rejected. The managed `images/` tree is also host-only. This is not a general secret scanner: remove credentials from old guest disks and audit other shared files. Same-user host processes and approved providers remain trusted.

## Usage

```sh
contain [run] [options]
contain create [--image NAME] [--size 8G] [--tools]
contain list
contain remove NAME
```

`run` is the default subcommand: `contain --shell` is equivalent to `contain run --shell`. The session flags below belong to `run`; `create` has its own flags. Use `contain run --help` or `contain create --help` for command-specific help and `contain --version` for version information.

| Flag | Description |
| --- | --- |
| `--image NAME`, `-i NAME` | Select an image from `~/.local/contain/images/` (default: `alpine`). Also available on `create`. |
| `--shell` | Start an interactive shell instead of the configured command. |
| `--plain` | Start an interactive shell at `/`, skipping all workspace, configured and CLI mounts, snapshots and startup commands. Takes precedence over `--shell`, `--run` and `--mount`; can be combined with `--temporary` and `--dry-run`. |
| `--run COMMAND` | Run a single-line shell command instead of the configured command. Cannot be combined with `--shell`. |
| `--mount HOST[:NAME][:ro\|rw]` | Add a session-only mount under `/projects/<name>`. Repeatable; defaults to read/write and infers `NAME` from the host directory basename. |
| `--temporary`, `--temp` | Discard guest disk changes on exit using an OpenVMM memory-backed diff layer. `disk.raw` stays untouched; writable host mounts still persist. |
| `--dry-run`, `--dry` | Validate config and flags, then print the resolved mounts, whether they hide existing guest files, snapshots, disk mode and command without starting the VM. |
| `--help` | Show command-specific help. |

Dry-run inspects `disk.raw` read-only as an offline preview (it does not account for paths created or changed during boot and does not change mounting behavior). In the `OVERRIDES FILES` column:

- `yes` - the mount destination contains any file (including an empty file), symlink or other non-directory entry anywhere below it; trees containing only empty directories count as empty.
- `n/a (parent mount)` - a `writable` override covering a host export rather than the guest disk.
- `unknown` - an unreadable destination or a symlink in the destination path (shown with a reason).

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
contain create --image dev --tools
contain create --image general --kernel lts
contain create --image testing --kernel stable
contain --image dev        # run the named image
contain list               # list images and their size
contain remove dev         # type dev again to confirm permanent deletion
```

This downloads the latest stable Alpine netboot kernel, initramfs and matching module archive, formats a raw ext4 disk in Go (minimum 512 MiB, binary units) and installs `alpine-base`, the selected kernel, Bash (default login shell, including without `--tools`), `ca-certificates` and `iproute2` inside a RAM-booted VM. `--kernel virt` (default) installs the VM-optimized `linux-virt`; `--kernel lts` installs the general-purpose `linux-lts`; `--kernel stable` installs the newer `linux-stable` from Alpine's tagged `edge/community` repository for testing new kernel features. The base system and tools continue to use the stable Alpine release. All variations use the same installation and setup. Installed boot files are exported over virtio-fs; no host-side Linux formatting or mounting tools are needed.

Internet access is required on the host and in the guest: provisioning deliberately retains unrestricted Consomme networking to install Alpine before the normal-session gateway exists (`machine.network` applies to normal sessions, not `contain create`).

With `--tools`, an extra step installs common packages (Python, C/C++ build tools, Git and shell utilities) and the latest [Tap](https://github.com/coalaura/tap), then uses `tap get` for Go, PACE, Zig, Bun's musl build, builder, vet and ffwebp's full build, adding their binary directories to the guest's login `PATH`.

The completed `disk.raw`, `vmlinuz-virt`, `initramfs-virt` and private `network/ca/` directory are stored in `~/.local/contain/images/<name>/` (default name `alpine`, login `root` / `alpine`). The exported boot filenames stay the same for all kernel variations; their contents match the selected kernel. Both `create` and `run` accept `--image NAME`, defaulting to `alpine`; image selection is session-only and replaces the old `machine.source` setting, which is ignored. Names must start with a letter or digit and contain only ASCII letters, digits, hyphens or underscores. Existing image directories are refused. Temporary guest-helper and snapshot exports are created inside the selected image directory and removed after each session and installer output is captured in `~/.local/contain/logs/contain-create-<session>.log` using a 12-character lowercase hex session ID.

`contain list` shows images alphabetically with the total logical size of their regular files in binary units (including the full provisioned disk size, rather than host space allocated to sparse files). `contain remove NAME` permanently deletes the entire image directory, including its disk, boot files and private CA, only after you type the exact full name at the confirmation prompt. Listing and removal do not require OpenVMM or a configuration file.

The `alpine/` directory also includes helper scripts for building an image on a Linux host:

```sh
cd alpine
./setup-deps.sh
./build-alpine.sh
```

This produces `disk.raw`, `vmlinuz-virt` and `initramfs-virt` in `alpine/build/` with default login `root` / `alpine`. Move that directory to `~/.local/contain/images/<name>/` and select it with `contain --image NAME`. Move any images previously selected through `machine.source` into this directory layout as well.

An optional `./prepare-vm.sh` script is also included to preinstall common development toolchains into a freshly built `disk.raw`.

## Notes

- Console communication uses a per-session named pipe on Windows and a Unix domain socket on Linux and macOS.
- Terminal dimensions sync at startup and on host resize events (`SIGWINCH` on Unix, console input events on Windows). A bundled Go helper receives updates over COM2 and resizes `/dev/hvc0`; existing images do not need rebuilding.
- Empty mountpoint directories are removed automatically on unmount and normal guest shutdown; their parent directories are preserved.

### Logging

Session logs are written to `~/.local/contain/logs/contain-<session>.log` and network logs to `contain-network-<session>.log`, sharing a 12-character lowercase hex session ID printed at startup. Network logs record UTC timestamps, gateway lifecycle events, TCP/UDP connection decisions, ICMP results, packet drops, DNS results (including cache hits) and intercepted TLS/HTTPS outcomes (host, method, status and elapsed time on completion, including streaming responses; transparent HTTPS logs connection-level entries only).

Headers, URL paths/queries, credentials and payloads are never logged and peer-controlled names are bounded and escaped. Logging is capped at 100 events per second, writing a suppressed-event count on the next event after the window ends or at shutdown.

Each session keeps a current `.log` and previous `.1.log` for both session and network logs (at most four files total). When a current file reaches `logs.max_size_mib` (default 10 MiB, range 1-1024), `.1.log` is deleted, `.log` becomes `.1.log` and a new `.log` is opened. Installer logs use the same rotation with a fixed 10 MiB limit because `contain create` does not load the VM configuration. Completed sessions' logs remain on disk and rotation limits apply separately per session.
