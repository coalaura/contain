#!/usr/bin/env bash
set -euo pipefail

# Build a minimal, directly bootable Alpine Linux disk for OpenVMM.
#
# Output (and only runtime artifacts created by this script):
#   disk.raw
#   vmlinuz-virt
#   initramfs-virt
#
# Usage:
#   ./build-alpine.sh [output-directory]
#
# Optional environment variables:
#   DISK_SIZE=8G            Sparse ext4 disk size.
#   ROOT_PASSWORD=alpine    Console root password.
#   GUEST_HOSTNAME=contained Guest hostname.
#   ALPINE_MIRROR=https://dl-cdn.alpinelinux.org/alpine
#
# This builder intentionally supports x86_64 only. The target rootfs is
# chrooted during construction, so target and host architecture must match.

OUTDIR="${1:-./build}"
DISK_SIZE="${DISK_SIZE:-8G}"
ROOT_PASSWORD="${ROOT_PASSWORD:-alpine}"
GUEST_HOSTNAME="${GUEST_HOSTNAME:-contained}"
ALPINE_MIRROR="${ALPINE_MIRROR:-https://dl-cdn.alpinelinux.org/alpine}"

case "$(uname -m)" in
    x86_64|amd64) ARCH=x86_64 ;;
    *)
        echo "error: this minimal builder currently supports x86_64 hosts only" >&2
        exit 1
        ;;
esac

required=(curl tar gzip sha256sum truncate chroot mkfs.ext4 mount umount grep sed awk sort)
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
    echo "error: build requires root privileges for loop mounting/chroot; install sudo or run as root" >&2
    exit 1
fi

WORK="$(mktemp -d)"
MNT="$WORK/root"
DEV_BOUND=0
PROC_BOUND=0
SYS_BOUND=0
MOUNTED=0

cleanup() {
    set +e
    if ((SYS_BOUND)); then
        "${SUDO[@]}" umount "$MNT/sys" >/dev/null 2>&1 || true
    fi
    if ((PROC_BOUND)); then
        "${SUDO[@]}" umount "$MNT/proc" >/dev/null 2>&1 || true
    fi
    if ((DEV_BOUND)); then
        "${SUDO[@]}" umount "$MNT/dev" >/dev/null 2>&1 || true
    fi
    if ((MOUNTED)); then
        "${SUDO[@]}" umount "$MNT" >/dev/null 2>&1 || true
    fi
    rm -rf "$WORK"
}
trap cleanup EXIT INT TERM

mkdir -p "$MNT" "$OUTDIR"

# Resolve the newest stable minirootfs from Alpine's official latest-stable
# directory. Restrict the match to final x.y.z releases (no rc/beta builds).
INDEX_URL="$ALPINE_MIRROR/latest-stable/releases/$ARCH/"
echo "Resolving latest stable Alpine release..."
MINIROOTFS="$(
    curl -fsSL --retry 3 "$INDEX_URL" |
        grep -oE "alpine-minirootfs-[0-9]+\.[0-9]+\.[0-9]+-${ARCH}\.tar\.gz" |
        sort -Vu |
        tail -n 1
)"

if [[ -z "$MINIROOTFS" ]]; then
    echo "error: could not determine latest Alpine minirootfs from $INDEX_URL" >&2
    exit 1
fi

VERSION="${MINIROOTFS#alpine-minirootfs-}"
VERSION="${VERSION%-${ARCH}.tar.gz}"
BRANCH="v${VERSION%.*}"
BASE_URL="$ALPINE_MIRROR/$BRANCH/releases/$ARCH"

echo "Alpine: $VERSION ($ARCH)"
echo "Disk size: $DISK_SIZE (sparse)"

curl -fL --retry 3 -o "$WORK/$MINIROOTFS" "$BASE_URL/$MINIROOTFS"
curl -fL --retry 3 -o "$WORK/$MINIROOTFS.sha256" "$BASE_URL/$MINIROOTFS.sha256"

(
    cd "$WORK"
    sha256sum -c "$MINIROOTFS.sha256"
)

# A raw ext4 filesystem is enough for direct boot. No GPT, EFI partition,
# bootloader, cloud-init disk, or firmware image is needed.
truncate -s "$DISK_SIZE" "$WORK/disk.raw"
mkfs.ext4 -F -q -L alpine-root "$WORK/disk.raw"

"${SUDO[@]}" mount -o loop "$WORK/disk.raw" "$MNT"
MOUNTED=1

# Preserve the root ownership/modes from the official Alpine minirootfs.
"${SUDO[@]}" tar --numeric-owner -xzf "$WORK/$MINIROOTFS" -C "$MNT"

# Pin package repositories to the same stable branch as the downloaded rootfs.
"${SUDO[@]}" tee "$MNT/etc/apk/repositories" >/dev/null <<EOF_REPOS
$ALPINE_MIRROR/$BRANCH/main
$ALPINE_MIRROR/$BRANCH/community
EOF_REPOS

# Networking for apk while chrooted.
"${SUDO[@]}" cp -L /etc/resolv.conf "$MNT/etc/resolv.conf"

# Follow Alpine's documented chroot setup: expose the host's device,
# proc, and sys trees while installing packages and generating the initramfs.
"${SUDO[@]}" mount --bind /dev "$MNT/dev"
DEV_BOUND=1
"${SUDO[@]}" mount --bind /proc "$MNT/proc"
PROC_BOUND=1
"${SUDO[@]}" mount --bind /sys "$MNT/sys"
SYS_BOUND=1

# Turn minirootfs into a bootable Alpine installation and install the VM kernel.
"${SUDO[@]}" chroot "$MNT" /bin/sh -eux <<'EOF_CHROOT'
apk update
apk add --no-cache alpine-base linux-virt bash ca-certificates iproute2
sed -i 's|^\(root:[^:]*:[^:]*:[^:]*:[^:]*:[^:]*:\).*|\1/bin/bash|' /etc/passwd

cat > /root/.bashrc <<'BASHRC'
alias ls='ls --color=auto'
alias ll='ls -l'
alias la='ls -la'
alias ..='cd ..'
alias ...='cd ../..'

shopt -s checkwinsize
BASHRC

cat > /root/.bash_profile <<'BASH_PROFILE'
. "$HOME/.bashrc"
BASH_PROFILE

# Essential OpenRC services for a disk-installed VM.
add_service() {
    service="$1"
    runlevel="$2"
    if [ -x "/etc/init.d/$service" ]; then
        rc-update add "$service" "$runlevel"
    fi
}

add_service devfs sysinit
add_service dmesg sysinit
add_service mdev sysinit
add_service hwdrivers sysinit

add_service modules boot
add_service sysctl boot
add_service hostname boot
add_service bootmisc boot
add_service syslog boot
add_service networking boot
add_service seedrng boot

add_service mount-ro shutdown
add_service killprocs shutdown
add_service savecache shutdown
EOF_CHROOT

# Configure the VM itself offline.
printf '%s\n' "$GUEST_HOSTNAME" | "${SUDO[@]}" tee "$MNT/etc/hostname" >/dev/null

"${SUDO[@]}" mkdir -p "$MNT/etc/network"
"${SUDO[@]}" tee "$MNT/etc/network/interfaces" >/dev/null <<'EOF_NETWORK'
auto lo
iface lo inet loopback

EOF_NETWORK

# The raw file is the filesystem itself, so OpenVMM exposes the root as /dev/vda.
"${SUDO[@]}" tee "$MNT/etc/fstab" >/dev/null <<'EOF_FSTAB'
/dev/vda / ext4 rw,relatime 0 0
EOF_FSTAB

# OpenVMM's virtio console appears as /dev/hvc0. Alpine does not create this
# getty by default.
if ! "${SUDO[@]}" grep -q '^hvc0:' "$MNT/etc/inittab"; then
    echo 'hvc0::respawn:/sbin/getty 115200 hvc0' |
        "${SUDO[@]}" tee -a "$MNT/etc/inittab" >/dev/null
fi

# Set the console root password without putting it in the chroot command line.
printf 'root:%s\n' "$ROOT_PASSWORD" | "${SUDO[@]}" chroot "$MNT" chpasswd

# The linux-virt package generates these as part of installation.
for f in boot/vmlinuz-virt boot/initramfs-virt; do
    if [[ ! -s "$MNT/$f" ]]; then
        echo "error: expected /$f was not generated" >&2
        exit 1
    fi
done

"${SUDO[@]}" cp "$MNT/boot/vmlinuz-virt" "$WORK/vmlinuz-virt"
"${SUDO[@]}" cp "$MNT/boot/initramfs-virt" "$WORK/initramfs-virt"
"${SUDO[@]}" chown "$(id -u):$(id -g)" "$WORK/vmlinuz-virt" "$WORK/initramfs-virt"
chmod 0644 "$WORK/vmlinuz-virt" "$WORK/initramfs-virt"

sync

"${SUDO[@]}" umount "$MNT/sys"
SYS_BOUND=0
"${SUDO[@]}" umount "$MNT/proc"
PROC_BOUND=0
"${SUDO[@]}" umount "$MNT/dev"
DEV_BOUND=0
"${SUDO[@]}" umount "$MNT"
MOUNTED=0

# Replace only our runtime artifacts. Temporary downloads/rootfs state stay in
# $WORK and are removed by the cleanup trap.
rm -f "$OUTDIR/disk.raw" "$OUTDIR/vmlinuz-virt" "$OUTDIR/initramfs-virt"
mv "$WORK/disk.raw" "$OUTDIR/disk.raw"
mv "$WORK/vmlinuz-virt" "$OUTDIR/vmlinuz-virt"
mv "$WORK/initramfs-virt" "$OUTDIR/initramfs-virt"

echo
echo "Built Alpine $VERSION in: $OUTDIR"
echo "  disk.raw"
echo "  vmlinuz-virt"
echo "  initramfs-virt"
echo
echo "Default console login: root / $ROOT_PASSWORD"
echo "OpenVMM kernel args: root=/dev/vda rootfstype=ext4 rw modules=virtio_pci,virtio_blk,ext4 console=hvc0"
