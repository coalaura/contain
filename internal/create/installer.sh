#!/bin/sh

# Temporary boot-relative timing for installer comparisons.
phase() {
	read -r uptime idle < /proc/uptime
	printf '[contain-install %ss] %s\n' "$uptime" "$*"
}

wait_for() {
	local description="$1" uptime idle deadline
	shift
	read -r uptime idle < /proc/uptime
	deadline=$((${uptime%%.*} + 10))

	until "$@"; do
		read -r uptime idle < /proc/uptime
		if [ "${uptime%%.*}" -ge "$deadline" ]; then
			printf 'Timed out waiting for %s\n' "$description" >&2
			return 1
		fi

		sleep 0.05
	done
}

find_disks() {
	local block serial
	for block in /sys/block/vd*; do
		[ -r "$block/serial" ] || continue
		# Virtio serial attributes can omit the trailing newline.
		read -r serial < "$block/serial" || [ -n "$serial" ] || continue
		case "$serial" in
			contain-root) target_disk="/dev/${block##*/}" ;;
			contain-modloop) modloop_disk="/dev/${block##*/}" ;;
		esac
	done

	[ -b "$target_disk" ] && [ -b "$modloop_disk" ]
}

# Stay PID 1 while the strict child runs, so failures can be reported and the
# VM can power off without an init-exited kernel panic.
if [ "${1:-}" != install ]; then
	/bin/sh "$0" install
	status=$?
	/bin/busybox sync
	printf '\n@STATUS_MARKER@%d\n' "$status"
	/bin/busybox poweroff -f
	while :; do
		/bin/busybox sleep 1
	done
fi

set -eu

export PATH=/usr/sbin:/usr/bin:/sbin:/bin
/bin/busybox --install -s
mkdir -p /proc /sys /dev /run /tmp /mnt /.modloop
mount -t proc proc /proc
phase 'installer start'

mount -t sysfs sysfs /sys
mount -t devtmpfs devtmpfs /dev
chmod 1777 /tmp

modprobe -a virtio_pci virtio_blk virtio_net squashfs

target_disk=
modloop_disk=
wait_for 'installer disks' find_disks
phase 'devices ready'

read -r readonly < "/sys/class/block/${modloop_disk##*/}/ro"
test "$readonly" = 1
mount -t squashfs -o ro "$modloop_disk" /.modloop
test -d "/.modloop/modules/$(uname -r)"
mount --bind /.modloop/modules /lib/modules
modprobe -a ext4 virtiofs
phase 'modules ready'

wait_for 'installer network' test -d /sys/class/net/eth0
ip link set lo up
ip link set eth0 up
timeout 15 udhcpc -i eth0 -s /usr/share/udhcpc/default.script -f -n -q -t 5 -T 1
phase 'network configured'

mount -t ext4 "$target_disk" /mnt
phase 'target mounted'

mkdir -p /mnt/etc/apk/keys /mnt/etc/mkinitfs /mnt/dev /mnt/proc /mnt/sys /mnt/tmp
chmod 1777 /mnt/tmp
cp /etc/apk/keys/* /mnt/etc/apk/keys/
cp /etc/apk/repositories /mnt/etc/apk/repositories
cp /etc/resolv.conf /mnt/etc/resolv.conf
printf 'features="base ext4 virtio"\n' > /mnt/etc/mkinitfs/mkinitfs.conf

mount --bind /dev /mnt/dev
mount --bind /proc /mnt/proc
mount --bind /sys /mnt/sys

apk --root /mnt --initdb --no-cache add alpine-base @KERNEL_PACKAGE@ bash ca-certificates iproute2
phase 'packages installed'

mkdir -p /mnt/usr/local/share/ca-certificates
cp /contain.crt /mnt/usr/local/share/ca-certificates/contain.crt
chroot /mnt update-ca-certificates

chroot /mnt /bin/sh -eu <<'CONFIGURE'
for service in devfs dmesg mdev hwdrivers; do
	rc-update add "$service" sysinit
done

for service in modules sysctl hostname bootmisc syslog networking seedrng; do
	rc-update add "$service" boot
done

for service in mount-ro killprocs savecache; do
	rc-update add "$service" shutdown
done

printf 'contained\n' > /etc/hostname
printf '127.0.0.1 localhost contained\n::1 localhost contained\n' > /etc/hosts

mkdir -p /etc/network
cat > /etc/network/interfaces <<'NETWORK'
auto lo
iface lo inet loopback

NETWORK

printf '/dev/vda / ext4 rw,relatime 0 0\n' > /etc/fstab
printf '\nhvc0::respawn:/sbin/getty 115200 hvc0\n' >> /etc/inittab
printf 'root:alpine\n' | chpasswd
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

test -s /boot/vmlinuz-@KERNEL@
test -s /boot/initramfs-@KERNEL@
CONFIGURE
phase 'system configured'

if [ -f /contain-tools.sh ]; then
	chroot /mnt /bin/sh -eu < /contain-tools.sh
fi

phase 'tools complete'

mkdir -p /export
mount -t virtiofs contain_export /export
# Keep the host-side filenames stable regardless of the installed variation.
cp /mnt/boot/vmlinuz-@KERNEL@ /export/vmlinuz-virt
cp /mnt/boot/initramfs-@KERNEL@ /export/initramfs-virt
phase 'boot files exported'

sync
umount /export
umount /mnt/sys
umount /mnt/proc
umount /mnt/dev
umount /mnt
umount /lib/modules
umount /.modloop
phase 'filesystems unmounted'
