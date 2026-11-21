shutdown_guest() {
	trap '' HUP INT TERM

	printf 'Shutting down...\n' >&2

	poweroff
}

trap shutdown_guest EXIT
