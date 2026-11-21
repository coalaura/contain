set -eu
umask 077

cleanup() {
	[ -z "$temporary" ] || rm -f -- "$temporary"
	if [ "$mounted" = yes ]; then
		umount "$staging" || return
	fi
	rmdir "$staging"
}

staging=$(mktemp -d /run/contain-snapshots.XXXXXX)
mounted=no
temporary=
trap cleanup EXIT
trap 'exit 1' HUP INT TERM
mount -t virtiofs -o ro contain_snapshots "$staging"
mounted=yes
index=0

for target in "$@"; do
	parent=${target%/*}
	[ -n "$parent" ] || parent=/
	mkdir -p -- "$parent"
	# Replace the directory entry, rather than modifying a possibly hard-linked inode.
	temporary=$(mktemp "$parent/.contain-snapshot.XXXXXX")
	cat "$staging/$index" > "$temporary"
	chmod 600 "$temporary"
	mv -fT -- "$temporary" "$target"
	temporary=
	index=$((index + 1))
done

umount "$staging"
mounted=no
