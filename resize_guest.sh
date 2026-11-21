set -eu

exec <"$1" >"$1"
printf '__CONTAIN_RESIZE:0\n'

while read -r rows columns; do
	status=1

	if [[ $rows =~ ^[0-9]{1,5}$ && $columns =~ ^[0-9]{1,5}$ ]]; then
		if (( 10#$rows > 0 && 10#$rows <= 65535 && 10#$columns > 0 && 10#$columns <= 65535 )); then
			status=0
			stty -F "$2" rows "$rows" cols "$columns" 2>/dev/null || status=$?
		fi
	fi

	printf '__CONTAIN_RESIZE:%d\n' "$status"
done
