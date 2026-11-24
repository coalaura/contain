set -e
trap 'printf "Guest network setup failed: %s\n" "$step" >&2' ERR

step='check image tools'
command -v update-ca-certificates >/dev/null && command -v ip >/dev/null || {
	printf '%s\n' 'Image requires ca-certificates and an IPv6-capable ip command (BusyBox or iproute2).' >&2
	exit 1
}

step='copy guest helper'
cp /run/contain-guest/resize /run/contain-resize
chmod 700 /run/contain-resize

step='install public CA'
mkdir -p /usr/local/share/ca-certificates
cp /run/contain-guest/contain.crt /usr/local/share/ca-certificates/contain.crt

step='update guest trust store'
update-ca-certificates

step='start vsock/TUN helper'
/run/contain-resize --network
