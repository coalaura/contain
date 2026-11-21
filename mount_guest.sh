fail() {
	printf '%s: %s\n' "$1" "$2" >&2
	exit 1
}

# 0: our export, 1: unmounted, 2: another/stacked mount, 3: unknown.
mount_state() {
	[ -r /proc/self/mountinfo ] || return 3
	state=1
	submounts=0

	while IFS=' ' read -r mount_id parent_id device root mounted_path rest; do
		case "$mounted_path" in
			*'\'*)
				# The sentinel preserves trailing newlines in decoded paths.
				mounted_path=$(printf '%b/' "$mounted_path")
				mounted_path=${mounted_path%/}
				;;
		esac

		case "$mounted_path" in
			"$1"/*) submounts=1 ;;
		esac

		[ "$mounted_path" = "$1" ] || continue
		[ "$state" -eq 1 ] || return 2

		case "$rest" in
			*" - virtiofs $2 "*) state=0 ;;
			*) return 2 ;;
		esac
	done < /proc/self/mountinfo || return 3

	return "$state"
}

remove_mountpoint() {
	# Override directories belong to the host, even when they are empty.
	[ -z "$2" ] || return 0
	rmdir "$1" 2>/dev/null || :
}

cleanup_mounts() {
	while [ "$#" -gt 0 ]; do
		mount_state "$1" "$2"

		case "$?" in
			0)
				# Leave parents alone if a foreign or busy child mount remains.
				if [ "$submounts" -eq 0 ]; then
					umount -l "$1" && remove_mountpoint "$1" "$4"
				fi
				;;
			1) remove_mountpoint "$1" "$4" ;;
		esac

		shift 4
	done
}

watch_mounts() {
	stopping=0
	trap - EXIT
	trap 'stopping=1' HUP INT TERM

	while [ "$#" -gt 0 ] && [ "$stopping" -eq 0 ]; do
		remaining=$#

		while [ "$remaining" -gt 0 ]; do
			mount_state "$1" "$2"

			if [ "$?" -eq 1 ]; then
				remove_mountpoint "$1" "$4"
			else
				set -- "$@" "$1" "$2" "$3" "$4"
			fi

			shift 4
			remaining=$((remaining - 4))
		done

		[ "$#" -eq 0 ] || [ "$stopping" -eq 1 ] || sleep 1
	done

	trap '' HUP INT TERM
	cleanup_mounts "$@"
}

mount_exports() {
	overrides=$1
	shift

	while [ "$#" -gt 0 ]; do
		if [ "$overrides" = no ] && [ -z "$4" ]; then
			mkdir -p "$1" || fail 'cannot create guest mount destination' "$1"
			mount -t virtiofs -o "$3" "$2" "$1" || fail 'cannot mount virtio-fs export' "$1"
		elif [ "$overrides" = yes ] && [ -n "$4" ]; then
			mount -t virtiofs -o rw "$2" "$1" || fail 'cannot mount writable export' "$1"
		fi

		shift 4
	done
}

trap 'cleanup_mounts "$@"' EXIT
trap 'exit 1' HUP INT TERM

# Keep the original arguments available for rollback if setup fails midway.
(
	trap - EXIT HUP INT TERM
	mkdir -p /projects || fail 'cannot create guest projects directory' /projects

	mount_exports no "$@"
	mount_exports yes "$@"
) || exit 1

# The guest shutdown signal reaches this process before filesystems go read-only.
watch_mounts "$@" </dev/null >/dev/null 2>&1 &
trap - EXIT HUP INT TERM
