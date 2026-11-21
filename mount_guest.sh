fail() {
	printf '%s: %s\n' "$1" "$2" >&2
	exit 1
}

plain_directory() {
	case "$1" in
		/) return 0 ;;
		/*) ;;
		*) return 1 ;;
	esac

	case "$1/" in
		*//*|*/./*|*/../*) return 1 ;;
	esac

	directory=$1

	while [ "$directory" != / ]; do
		[ ! -L "$directory" ] && { [ ! -e "$directory" ] || [ -d "$directory" ]; } || return 1
		directory=${directory%/*}
		[ -n "$directory" ] || directory=/
	done
}

resolve_mount_path() {
	resolved=$1

	case "$resolved" in
		'~/.config/'*)
			[ -n "$HOME" ] || fail 'guest HOME is not set for config mounts' "$1"
			resolved=${HOME%/}/.config/${resolved#'~/.config/'}
			;;
	esac
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
	plain_directory "$1" || return 0
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

check_mount_overlap() {
	outer=$1
	shift 4

	while [ "$#" -gt 0 ]; do
		if [ -z "$4" ]; then
			case "$1/" in
				"$outer/"*) fail 'overlapping guest mount destinations' "$1" ;;
			esac

			case "$outer/" in
				"$1/"*) fail 'overlapping guest mount destinations' "$outer" ;;
			esac
		fi

		shift 4
	done
}

check_mountpoints() {
	while [ "$#" -gt 0 ]; do
		if [ -n "$4" ]; then
			shift 4
			continue
		fi

		# Resolving HOME can reveal overlaps that the host could not check.
		check_mount_overlap "$@"
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

		shift 4
	done
}

check_overrides() {
	while [ "$#" -gt 0 ]; do
		if [ -n "$4" ]; then
			current=$1

			while :; do
				[ ! -L "$current" ] && [ -d "$current" ] || fail 'writable guest path is missing, symbolic or not a directory' "$current"
				[ "$current" != "$4" ] || break
				current=${current%/*}
			done

			mount_state "$1" "$2"

			case "$?" in
				0|2) fail 'writable guest destination is already mounted' "$1" ;;
				3) fail 'cannot inspect guest mounts' /proc/self/mountinfo ;;
			esac
		fi

		shift 4
	done
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

# Resolve guest-relative paths once so setup and cleanup use identical destinations.
remaining=$#

while [ "$remaining" -gt 0 ]; do
	resolve_mount_path "$1"
	destination=$resolved
	resolve_mount_path "$4"
	set -- "$@" "$destination" "$2" "$3" "$resolved"
	shift 4
	remaining=$((remaining - 4))
done

# Check top-level destinations before creating directories or exposing exports.
plain_directory /projects || fail 'symbolic link or non-directory at guest projects path' /projects
check_mountpoints "$@"
trap 'cleanup_mounts "$@"' EXIT
trap 'exit 1' HUP INT TERM

# Keep the original arguments available for rollback if setup fails midway.
(
	trap - EXIT HUP INT TERM
	mkdir -p /projects || fail 'cannot create guest projects directory' /projects

	mount_exports no "$@"
	check_overrides "$@"
	mount_exports yes "$@"
) || exit 1

# The guest shutdown signal reaches this process before filesystems go read-only.
watch_mounts "$@" </dev/null >/dev/null 2>&1 &
trap - EXIT HUP INT TERM
