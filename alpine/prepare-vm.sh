#!/usr/bin/env bash
set -euo pipefail

# Provision an Alpine OpenVMM disk image from the host.
#
# Usage:
#   ./prepare-vm.sh [./build | ./build/disk.raw]
#
# This script is intended for a freshly-built/trusted image. It uses chroot,
# which is NOT a security boundary. Do not run it against an image that may
# have been modified by an untrusted agent; boot that image in the VM instead.

TARGET="${1:-./build}"
if [[ -d "$TARGET" ]]; then
    DISK="$TARGET/disk.raw"
else
    DISK="$TARGET"
fi

if [[ ! -f "$DISK" ]]; then
    echo "error: disk image not found: $DISK" >&2
    exit 1
fi

required=(mount umount chroot mktemp mknod cp rm mkdir sync)
missing=()
for cmd in "${required[@]}"; do
    command -v "$cmd" >/dev/null 2>&1 || missing+=("$cmd")
done
if ((${#missing[@]})); then
    echo "error: missing host tools: ${missing[*]}" >&2
    echo "run ./setup-deps.sh first" >&2
    exit 1
fi

if [[ ${EUID:-$(id -u)} -eq 0 ]]; then
    SUDO=()
elif command -v sudo >/dev/null 2>&1; then
    SUDO=(sudo)
else
    echo "error: provisioning requires root privileges; install sudo or run as root" >&2
    exit 1
fi

WORK="$(mktemp -d)"
MNT="$WORK/root"
ROOT_MOUNTED=0
DEV_MOUNTED=0
PROC_MOUNTED=0
RESOLV_SAVED=0
mkdir -p "$MNT"

cleanup() {
    set +e

    if ((PROC_MOUNTED)); then
        "${SUDO[@]}" umount "$MNT/proc" >/dev/null 2>&1 || true
    fi
    if ((DEV_MOUNTED)); then
        "${SUDO[@]}" umount "$MNT/dev" >/dev/null 2>&1 || true
    fi

    if ((ROOT_MOUNTED)); then
        if [[ -e "$MNT/etc/resolv.conf" || -L "$MNT/etc/resolv.conf" ]]; then
            "${SUDO[@]}" rm -f "$MNT/etc/resolv.conf" >/dev/null 2>&1 || true
        fi
        if ((RESOLV_SAVED)); then
            "${SUDO[@]}" cp -a "$WORK/resolv.conf.saved" "$MNT/etc/resolv.conf" >/dev/null 2>&1 || true
        fi

        "${SUDO[@]}" umount "$MNT" >/dev/null 2>&1 || true
    fi

    rm -rf "$WORK"
}
trap cleanup EXIT INT TERM

echo "Mounting $DISK..."
"${SUDO[@]}" mount -o loop,rw "$DISK" "$MNT"
ROOT_MOUNTED=1

if [[ ! -f "$MNT/etc/alpine-release" || ! -x "$MNT/sbin/apk" ]]; then
    echo "error: image does not look like an Alpine root filesystem" >&2
    exit 1
fi

# Give the chroot working DNS, but restore the image's original resolver state
# before unmounting it.
if [[ -e "$MNT/etc/resolv.conf" || -L "$MNT/etc/resolv.conf" ]]; then
    "${SUDO[@]}" cp -a "$MNT/etc/resolv.conf" "$WORK/resolv.conf.saved"
    RESOLV_SAVED=1
fi
"${SUDO[@]}" rm -f "$MNT/etc/resolv.conf"
"${SUDO[@]}" cp -L /etc/resolv.conf "$MNT/etc/resolv.conf"

# Provide only the minimal pseudo-filesystems needed by installers and build
# tools. Avoid binding the host's full /dev or /sys tree into the guest image.
"${SUDO[@]}" mount -t tmpfs -o mode=755,nosuid tmpfs "$MNT/dev"
DEV_MOUNTED=1
"${SUDO[@]}" mknod -m 666 "$MNT/dev/null" c 1 3
"${SUDO[@]}" mknod -m 666 "$MNT/dev/zero" c 1 5
"${SUDO[@]}" mknod -m 666 "$MNT/dev/random" c 1 8
"${SUDO[@]}" mknod -m 666 "$MNT/dev/urandom" c 1 9
"${SUDO[@]}" mknod -m 666 "$MNT/dev/tty" c 5 0
"${SUDO[@]}" mkdir -p "$MNT/dev/shm"
"${SUDO[@]}" chmod 1777 "$MNT/dev/shm"

"${SUDO[@]}" mount -t proc -o nosuid,noexec,nodev proc "$MNT/proc"
PROC_MOUNTED=1

echo "Provisioning Alpine $(<"$MNT/etc/alpine-release")..."

"${SUDO[@]}" chroot "$MNT" /bin/sh -s <<'EOF_GUEST'
set -eu

export PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin

say() {
    printf '\n==> %s\n' "$*"
}

download() {
    curl -fsSL --proto '=https' --proto-redir '=https' --retry 3 --connect-timeout 30 "$@"
}

case "$(uname -m)" in
    x86_64|amd64)
        GOARCH=amd64
        BUN_ARCH=x64
        ;;
    aarch64|arm64)
        GOARCH=arm64
        BUN_ARCH=aarch64
        ;;
    *)
        echo "error: unsupported guest architecture: $(uname -m)" >&2
        exit 1
        ;;
esac

PACKAGES='
python3
py3-pip
py3-virtualenv
py3-setuptools
zig
gcc
g++
cmake
curl
bash
git
sudo
ca-certificates
tzdata
coreutils
findutils
grep
sed
gawk
diffutils
patch
file
which
less
tree
jq
fd
wget
openssl
gnupg
tar
gzip
bzip2
xz
zstd
zip
unzip
lsof
iputils
bind-tools
libstdc++
build-base
linux-headers
pkgconf
perl
shellcheck
shfmt
'

say "Installing missing Alpine packages"
missing_packages=''
for package in $PACKAGES; do
    if ! apk info -e "$package" >/dev/null 2>&1; then
        missing_packages="$missing_packages $package"
    fi
done

if [ -n "$missing_packages" ]; then
    # Word splitting is intentional: Alpine package names cannot contain spaces.
    # shellcheck disable=SC2086
    apk add --no-cache $missing_packages
else
    echo "All APK packages are already installed."
fi

update-ca-certificates >/dev/null 2>&1 || true

TMPROOT="$(mktemp -d)"
trap 'rm -rf "$TMPROOT"' EXIT HUP INT TERM

install_go_tree() (
    version="$1"
    destination="$2"

    wanted="go${version}"
    if [ -x "$destination/bin/go" ]; then
        current="$($destination/bin/go version 2>/dev/null | awk '{print $3}' || true)"
        if [ "$current" = "$wanted" ]; then
            echo "$wanted already installed at $destination"
            exit 0
        fi
    fi

    archive="go${version}.linux-${GOARCH}.tar.gz"
    stage="$(mktemp -d "$TMPROOT/go.XXXXXX")"
    archive_path="$stage/$archive"
    metadata="$stage/downloads.json"

    echo "Installing Go $version to $destination..."
    download -o "$metadata" 'https://go.dev/dl/?mode=json&include=all'

    checksum="$(
        jq -r --arg filename "$archive" \
            '[.[] | .files[] | select(.filename == $filename) | .sha256][0] // empty' \
            "$metadata"
    )"

    if ! printf '%s\n' "$checksum" | grep -Eq '^[0-9a-fA-F]{64}$'; then
        echo "error: could not resolve SHA-256 for $archive" >&2
        exit 1
    fi

    download -o "$archive_path" "https://go.dev/dl/$archive"
    printf '%s  %s\n' "$checksum" "$archive_path" | sha256sum -c -

    mkdir -p "$stage/extract"
    tar -C "$stage/extract" -xzf "$archive_path"
    test -x "$stage/extract/go/bin/go"

    rm -rf "$destination"
    mkdir -p "$(dirname "$destination")"
    mv "$stage/extract/go" "$destination"
)

say "Installing current stable Go"
LATEST_GO_TAG="$(download 'https://go.dev/VERSION?m=text' | sed -n '1p')"
case "$LATEST_GO_TAG" in
    go[0-9]*.[0-9]*.[0-9]*) ;;
    *)
        echo "error: unexpected latest Go version: $LATEST_GO_TAG" >&2
        exit 1
        ;;
esac
LATEST_GO_VERSION="${LATEST_GO_TAG#go}"
install_go_tree "$LATEST_GO_VERSION" /usr/local/go

# Wrappers keep /usr/local/bin as the stable PATH entry while preserving the
# real GOROOT layout expected by the official toolchain.
cat >/usr/local/bin/go <<'EOF_GO_WRAPPER'
#!/bin/sh
exec /usr/local/go/bin/go "$@"
EOF_GO_WRAPPER
cat >/usr/local/bin/gofmt <<'EOF_GOFMT_WRAPPER'
#!/bin/sh
exec /usr/local/go/bin/gofmt "$@"
EOF_GOFMT_WRAPPER
chmod 0755 /usr/local/bin/go /usr/local/bin/gofmt

say "Installing Bun (musl build) if missing"
if command -v bun >/dev/null 2>&1; then
    echo "Bun already installed: $(bun --version)"
else
    BUN_REPO='https://github.com/oven-sh/bun'
    BUN_RELEASE="$(download -o /dev/null -w '%{url_effective}' "$BUN_REPO/releases/latest")"
    case "$BUN_RELEASE" in
        "$BUN_REPO/releases/tag/"*) BUN_TAG="${BUN_RELEASE##*/}" ;;
        *)
            echo "error: could not resolve latest Bun release" >&2
            exit 1
            ;;
    esac

    BUN_ASSET="bun-linux-${BUN_ARCH}-musl.zip"
    BUN_BASE="$BUN_REPO/releases/download/$BUN_TAG"
    BUN_STAGE="$(mktemp -d "$TMPROOT/bun.XXXXXX")"

    download -o "$BUN_STAGE/$BUN_ASSET" "$BUN_BASE/$BUN_ASSET"
    download -o "$BUN_STAGE/SHASUMS256.txt" "$BUN_BASE/SHASUMS256.txt"

    BUN_SHA="$(awk -v asset="$BUN_ASSET" '$2 == asset || $2 == "*" asset { print $1; exit }' "$BUN_STAGE/SHASUMS256.txt")"
    if ! printf '%s\n' "$BUN_SHA" | grep -Eq '^[0-9a-fA-F]{64}$'; then
        echo "error: missing or invalid Bun checksum for $BUN_ASSET" >&2
        exit 1
    fi

    printf '%s  %s\n' "$BUN_SHA" "$BUN_STAGE/$BUN_ASSET" | sha256sum -c -
    mkdir -p "$BUN_STAGE/extract"
    unzip -q "$BUN_STAGE/$BUN_ASSET" -d "$BUN_STAGE/extract"

    BUN_BIN="$(find "$BUN_STAGE/extract" -type f -name bun -print -quit)"
    test -n "$BUN_BIN"
    install -m 0755 "$BUN_BIN" /usr/local/bin/bun
fi

say "Installing latest PACE with its exact matching Go toolchain"
PACE_REPO='https://github.com/coalaura/pace'
PACE_RELEASE="$(download -o /dev/null -w '%{url_effective}' "$PACE_REPO/releases/latest")"
case "$PACE_RELEASE" in
    "$PACE_REPO/releases/tag/"*) PACE_TAG="${PACE_RELEASE##*/}" ;;
    *)
        echo "error: could not resolve latest PACE release" >&2
        exit 1
        ;;
esac
case "$PACE_TAG" in
    pace[0-9]*.[0-9]*.[0-9]*) ;;
    *)
        echo "error: unexpected PACE tag: $PACE_TAG" >&2
        exit 1
        ;;
esac

PACE_VERSION="${PACE_TAG#pace}"
PACE_ROOT=/opt/pace-go
install_go_tree "$PACE_VERSION" "$PACE_ROOT"

PACE_CURRENT=''
if [ -x "$PACE_ROOT/bin/pace" ]; then
    PACE_CURRENT="$($PACE_ROOT/bin/pace version 2>/dev/null || true)"
fi

case "$PACE_CURRENT" in
    *"go${PACE_VERSION}"*"(pace)"*)
        echo "PACE $PACE_VERSION already installed."
        ;;
    *)
        PACE_ASSET="pace-${PACE_VERSION}-linux-${GOARCH}.tar.gz"
        PACE_BASE="$PACE_REPO/releases/download/$PACE_TAG"
        PACE_STAGE="$(mktemp -d "$TMPROOT/pace.XXXXXX")"

        download -o "$PACE_STAGE/$PACE_ASSET" "$PACE_BASE/$PACE_ASSET"
        download -o "$PACE_STAGE/SHA256SUMS" "$PACE_BASE/SHA256SUMS"

        PACE_SHA="$(awk -v asset="$PACE_ASSET" '$2 == asset || $2 == "*" asset { print $1; exit }' "$PACE_STAGE/SHA256SUMS")"
        if ! printf '%s\n' "$PACE_SHA" | grep -Eq '^[0-9a-fA-F]{64}$'; then
            echo "error: missing or invalid PACE checksum for $PACE_ASSET" >&2
            exit 1
        fi

        printf '%s  %s\n' "$PACE_SHA" "$PACE_STAGE/$PACE_ASSET" | sha256sum -c -
        mkdir -p "$PACE_STAGE/extract"
        tar -C "$PACE_STAGE/extract" -xzf "$PACE_STAGE/$PACE_ASSET"

        PACE_BIN="$(find "$PACE_STAGE/extract" -type f -path '*/bin/pace' -print -quit)"
        test -n "$PACE_BIN"
        PACE_OVERLAY="${PACE_BIN%/bin/pace}"
        test -x "$PACE_OVERLAY/bin/compilepe"
        test -x "$PACE_OVERLAY/bin/asmpe"

        cp -a "$PACE_OVERLAY"/. "$PACE_ROOT"/
        ;;
esac

cat >/usr/local/bin/pace <<'EOF_PACE_WRAPPER'
#!/bin/sh
exec /opt/pace-go/bin/pace "$@"
EOF_PACE_WRAPPER
chmod 0755 /usr/local/bin/pace

install_coalaura_release() (
    project="$1"
    case "$project" in
        builder|vet|ffwebp) ;;
        *) echo "error: unsupported release project: $project" >&2; exit 1 ;;
    esac

    if [ "$project" = builder ] && [ "$GOARCH" != amd64 ]; then
        echo "error: coalaura/builder currently has no Linux $GOARCH release asset" >&2
        exit 1
    fi

    repository="https://github.com/coalaura/$project"
    latest="$(download -o /dev/null -w '%{url_effective}' "$repository/releases/latest")"
    case "$latest" in
        "$repository/releases/tag/"*) tag="${latest##*/}" ;;
        *) echo "error: could not resolve latest $project release: $latest" >&2; exit 1 ;;
    esac
    case "$tag" in
        v[0-9]*.[0-9]*.[0-9]*) ;;
        *) echo "error: unexpected $project release tag: $tag" >&2; exit 1 ;;
    esac

    case "$project" in
        builder|vet)
            asset="${project}-linux-${GOARCH}"
            ;;
        ffwebp)
            # The 'full' build is the equivalent of the previous -tags=full
            # source build. Avoid 'full-play': its Linux build requires glibc.
            asset="ffwebp_${tag}_full_linux_${GOARCH}"
            ;;
    esac

    base="$repository/releases/download/$tag"
    stage="$(mktemp -d "$TMPROOT/${project}.XXXXXX")"
    sums="$stage/SHA256SUMS"
    destination="/usr/local/bin/$project"

    echo "Checking $project $tag ($asset)..."
    download -o "$sums" "$base/SHA256SUMS"
    expected="$(awk -v asset="$asset" '$2 == asset || $2 == "*" asset { print $1; exit }' "$sums")"
    if ! printf '%s\n' "$expected" | grep -Eq '^[0-9a-fA-F]{64}$'; then
        echo "error: missing SHA-256 entry for $asset in $project $tag" >&2
        exit 1
    fi

    if [ -x "$destination" ]; then
        actual="$(sha256sum "$destination" | awk '{print $1}')"
        if [ "$actual" = "$expected" ]; then
            echo "$project $tag already installed."
            exit 0
        fi
    fi

    download -o "$stage/$asset" "$base/$asset"
    printf '%s  %s\n' "$expected" "$stage/$asset" | sha256sum -c -
    install -m 0755 "$stage/$asset" "$destination"
    echo "Installed $project $tag -> $destination"
)

say "Installing homemade binaries"
install_coalaura_release builder
install_coalaura_release vet
install_coalaura_release ffwebp

say "Verifying toolchain"
for command_name in \
    bun go gofmt pace python3 pip3 virtualenv zig gcc g++ cmake curl bash git sudo \
    find grep sed awk diff patch file which less tree jq fd wget openssl gpg tar gzip \
    bzip2 xz zstd zip unzip lsof ping nslookup pkg-config perl shellcheck shfmt \
    builder vet ffwebp
do
    if ! command -v "$command_name" >/dev/null 2>&1; then
        echo "error: expected command is missing after provisioning: $command_name" >&2
        exit 1
    fi
done

printf '\nProvisioned successfully.\n'
printf '  Alpine: %s\n' "$(cat /etc/alpine-release)"
printf '  Go:     %s\n' "$(go version)"
printf '  PACE:   %s\n' "$(pace version)"
printf '  Bun:    %s\n' "$(bun --version)"
printf '  Python: %s\n' "$(python3 --version 2>&1)"
printf '  Zig:    %s\n' "$(zig version)"

printf '\nfinalizing...\n'
EOF_GUEST

sync

# Restore resolver state while the root filesystem is still mounted.
"${SUDO[@]}" rm -f "$MNT/etc/resolv.conf"
if ((RESOLV_SAVED)); then
    "${SUDO[@]}" cp -a "$WORK/resolv.conf.saved" "$MNT/etc/resolv.conf"
    RESOLV_SAVED=0
fi

"${SUDO[@]}" umount "$MNT/proc"
PROC_MOUNTED=0
"${SUDO[@]}" umount "$MNT/dev"
DEV_MOUNTED=0
"${SUDO[@]}" umount "$MNT"
ROOT_MOUNTED=0

trap - EXIT INT TERM
rm -rf "$WORK"

echo
echo "Provisioning complete: $DISK"
