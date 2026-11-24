#!/bin/sh

# Stay PID 1 while the strict child runs, so failures can be reported and the
# VM can power off without an init-exited kernel panic.
if [ "${1:-}" != install ]; then
	/bin/sh "$0" install
	status=$?
	sync
	printf '\n@STATUS_MARKER@%d\n' "$status"
	poweroff -f
	while :; do sleep 1; done
fi

set -eu

# The netboot initramfs preloads loop and squashfs, but ext4 lives in modloop.
rc-update add modloop sysinit
openrc sysinit
modprobe ext4

mkdir -p /mnt
mount -t ext4 /dev/vda /mnt

mkdir -p /mnt/etc/apk/keys /mnt/etc/mkinitfs /mnt/dev /mnt/proc /mnt/sys /mnt/tmp
chmod 1777 /mnt/tmp
cp /etc/apk/keys/* /mnt/etc/apk/keys/
cp /etc/apk/repositories /mnt/etc/apk/repositories
cp /etc/resolv.conf /mnt/etc/resolv.conf
printf 'features="base ext4 virtio"\n' > /mnt/etc/mkinitfs/mkinitfs.conf

mount --bind /dev /mnt/dev
mount --bind /proc /mnt/proc
mount --bind /sys /mnt/sys

apk --root /mnt --initdb --no-cache add alpine-base linux-@KERNEL@ bash ca-certificates iproute2

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

if [ -f /contain-tools.sh ]; then
	chroot /mnt /bin/sh -eu < /contain-tools.sh
fi

mkdir -p /export
modprobe virtiofs
mount -t virtiofs contain_export /export
# Keep the host-side filenames stable regardless of the installed variation.
cp /mnt/boot/vmlinuz-@KERNEL@ /export/vmlinuz-virt
cp /mnt/boot/initramfs-@KERNEL@ /export/initramfs-virt

sync
umount /export
umount /mnt/sys
umount /mnt/proc
umount /mnt/dev
umount /mnt
