shutdown_guest() {
	trap '' HUP INT TERM
	PATH="$PATH:/sbin:/usr/sbin"

	if command -v poweroff >/dev/null 2>&1 && poweroff; then
		return
	fi

	if command -v systemctl >/dev/null 2>&1 && systemctl poweroff; then
		return
	fi

	if command -v shutdown >/dev/null 2>&1 && shutdown -h now; then
		return
	fi

	printf 'Unable to shut down the guest with poweroff, systemctl or shutdown.\n' >&2

	return 1
}

trap shutdown_guest EXIT
trap 'exit' HUP INT TERM
printf '%s%s_READY:0\n' "$2" "$3"

# Isolate the command so exit, exec and shell errors cannot bypass shutdown.
sh -c "$1"
