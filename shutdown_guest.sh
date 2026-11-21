shutdown_guest() {
	trap '' HUP INT TERM
	PATH="$PATH:/sbin:/usr/sbin"

	printf 'Shutting down...\n' >&2

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
