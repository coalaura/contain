fail() {
	printf '%s: %s\n' "$1" "$2" >&2
	exit 1
}

plain_directory() {
	[ ! -L "$1" ] && { [ ! -e "$1" ] || [ -d "$1" ]; }
}

# 0: our export, 1: unmounted, 2: another/stacked mount, 3: unknown.
mount_state() {
	[ -r /proc/self/mountinfo ] || return 3
	state=1

	while IFS=' ' read -r mount_id parent_id device root mounted_path rest; do
		case "$mounted_path" in
			*'\'*)
				# The sentinel preserves trailing newlines in decoded paths.
				mounted_path=$(printf '%b/' "$mounted_path")
				mounted_path=${mounted_path%/}
				;;
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
	plain_directory /projects && plain_directory "$1" || return 0
	rmdir "$1" 2>/dev/null || :
}

cleanup_mounts() {
	while [ "$#" -gt 0 ]; do
		mount_state "$1" "$2"

		case "$?" in
			0) umount -l "$1" && remove_mountpoint "$1" ;;
			1) remove_mountpoint "$1" ;;
		esac

		shift 3
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
				remove_mountpoint "$1"
			else
				set -- "$@" "$1" "$2" "$3"
			fi

			shift 3
			remaining=$((remaining - 3))
		done

		[ "$#" -eq 0 ] || [ "$stopping" -eq 1 ] || sleep 1
	done

	trap '' HUP INT TERM
	cleanup_mounts "$@"
}

check_mountpoints() {
	while [ "$#" -gt 0 ]; do
		plain_directory "$1" || fail 'symbolic link or non-directory at guest mount path' "$1"
		mount_state "$1" "$2"

		case "$?" in
			0|2) fail 'guest mount destination is already mounted' "$1" ;;
			3) fail 'cannot inspect guest mounts' /proc/self/mountinfo ;;
		esac

		for entry in "$1"/* "$1"/.[!.]* "$1"/..?*; do
			if [ -e "$entry" ] || [ -L "$entry" ]; then
				fail 'guest mount destination is not empty' "$1"
			fi
		done

		shift 3
	done
}

# Check every destination before creating directories or exposing any export.
plain_directory /projects || fail 'symbolic link or non-directory at guest projects path' /projects
check_mountpoints "$@"
trap 'cleanup_mounts "$@"' EXIT
trap 'exit 1' HUP INT TERM

# Keep the original arguments available for rollback if setup fails midway.
(
	trap - EXIT HUP INT TERM
	mkdir -p /projects || fail 'cannot create guest projects directory' /projects

	while [ "$#" -gt 0 ]; do
		mkdir -p "$1" || fail 'cannot create guest mount destination' "$1"
		mount -t virtiofs -o "$3" "$2" "$1" || fail 'cannot mount virtio-fs export' "$1"
		shift 3
	done
) || exit 1

# The guest shutdown signal reaches this process before filesystems go read-only.
watch_mounts "$@" </dev/null >/dev/null 2>&1 &
trap - EXIT HUP INT TERM
