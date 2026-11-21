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

apk --root /mnt --initdb --no-cache add alpine-base linux-virt

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

auto eth0
iface eth0 inet dhcp
NETWORK

printf '/dev/vda / ext4 rw,relatime 0 0\n' > /etc/fstab
printf '\nhvc0::respawn:/sbin/getty 115200 hvc0\n' >> /etc/inittab
printf 'root:alpine\n' | chpasswd

test -s /boot/vmlinuz-virt
test -s /boot/initramfs-virt
CONFIGURE

mkdir -p /export
modprobe virtiofs
mount -t virtiofs contain_export /export
cp /mnt/boot/vmlinuz-virt /mnt/boot/initramfs-virt /export/

sync
umount /export
umount /mnt/sys
umount /mnt/proc
umount /mnt/dev
umount /mnt
