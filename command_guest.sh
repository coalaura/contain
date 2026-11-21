trap 'exit' HUP INT TERM
printf '%s%s_READY:0\n' "$2" "$3"

# Isolate the command so exit, exec and shell errors cannot bypass shutdown.
bash -c "$1"
