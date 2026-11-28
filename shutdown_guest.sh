shutdown_guest() {
	local __contain_exit_status=$?
	trap '' HUP INT TERM

	# Split the session marker so echoed setup input cannot report an exit.
	printf '\036%s%s_EXIT:%d\037' '@LEFT@' '@RIGHT@' "$__contain_exit_status"
	printf 'Shutting down...\n' >&2

	# Release the working directory before OpenRC unmounts host filesystems.
	cd /
	: > /run/contain-poweroff || :
	poweroff
}

trap shutdown_guest EXIT
trap 'exit 129' HUP
