#!/usr/bin/env bash
set -euo pipefail

# Host-side dependencies required by build-alpine.sh.
# Supported package managers: paru, pacman, apt-get/apt.
# Safe to rerun: only packages backing missing commands are requested, and
# package-manager-level --needed/idempotency is used as well.

if [[ ${EUID:-$(id -u)} -eq 0 ]]; then
    SUDO=()
elif command -v sudo >/dev/null 2>&1; then
    SUDO=(sudo)
else
    echo "error: run this script as root, or install sudo first" >&2
    exit 1
fi

# command -> package. These package names are identical on Arch and Debian
# for the tools we need.
declare -A REQUIRED=(
    [curl]=curl
    [tar]=tar
    [gzip]=gzip
    [sha256sum]=coreutils
    [truncate]=coreutils
    [chroot]=coreutils
    [mkfs.ext4]=e2fsprogs
    [mount]=util-linux
    [umount]=util-linux
    [grep]=grep
    [sed]=sed
    [awk]=gawk
    [sort]=coreutils
)

packages=()
for cmd in "${!REQUIRED[@]}"; do
    if ! command -v "$cmd" >/dev/null 2>&1; then
        packages+=("${REQUIRED[$cmd]}")
    fi
done

# Deduplicate package names.
if ((${#packages[@]})); then
    mapfile -t packages < <(printf '%s\n' "${packages[@]}" | sort -u)
fi

if ((${#packages[@]} == 0)); then
    echo "All build dependencies are already installed."
    exit 0
fi

echo "Missing packages: ${packages[*]}"

if command -v pacman >/dev/null 2>&1; then
    echo "Using pacman."
    "${SUDO[@]}" pacman -S --needed --noconfirm -- "${packages[@]}"
elif command -v paru >/dev/null 2>&1; then
    echo "Using paru."
    paru -S --needed --noconfirm -- "${packages[@]}"
elif command -v apt-get >/dev/null 2>&1; then
    echo "Using apt-get."
    "${SUDO[@]}" apt-get update
    "${SUDO[@]}" apt-get install -y --no-install-recommends "${packages[@]}"
elif command -v apt >/dev/null 2>&1; then
    echo "Using apt."
    "${SUDO[@]}" apt update
    "${SUDO[@]}" apt install -y --no-install-recommends "${packages[@]}"
else
    echo "error: no supported package manager found (paru, pacman, apt-get, apt)" >&2
    exit 1
fi

echo "Dependencies installed."
