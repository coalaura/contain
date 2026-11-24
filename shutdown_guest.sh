shutdown_guest() {
	trap '' HUP INT TERM

	printf 'Shutting down...\n' >&2

	# Release the working directory before OpenRC unmounts host filesystems.
	cd /
	: > /run/contain-poweroff || :
	poweroff
}

trap shutdown_guest EXIT
