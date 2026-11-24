[ "$(readlink /proc/1/exe)" = /bin/busybox ] || exit 0
[ -f /etc/inittab ] && [ ! -L /etc/inittab ] || exit 0

# Only replace the stock Alpine action; preserve custom shutdown sequences.
actions=$(awk -F: '$3 == "shutdown" { print }' /etc/inittab) || exit 1
[ "$actions" = '::shutdown:/sbin/openrc shutdown' ] || exit 0

# The /run marker limits acceleration to contain's power-off requests, not reboots.
# Returning from this action leaves BusyBox's normal shutdown fallback intact.
sed -i "s|^::shutdown:/sbin/openrc shutdown$|::shutdown:/bin/sh -c '/sbin/openrc shutdown \&\& [ -f /run/contain-poweroff ] \&\& /bin/mount -o remount,ro / \&\& /bin/busybox poweroff -f'|" /etc/inittab || exit 1
kill -HUP 1
