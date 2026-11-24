#!/bin/sh
set -eu

export HOME=/root
export PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin

printf '\nInstalling common Alpine tools and dependencies...\n'
apk add --no-cache python3 py3-pip py3-virtualenv py3-setuptools gcc g++ cmake curl git openssh sudo ca-certificates tzdata coreutils findutils grep sed gawk diffutils patch file which less tree jq fd wget openssl gnupg tar gzip bzip2 xz zstd zip unzip lsof iputils bind-tools libstdc++ build-base linux-headers pkgconf perl shellcheck shfmt
update-ca-certificates

git config --system core.autocrlf input
git config --system core.untrackedCache true
git config --system diff.renames true
git config --system merge.conflictStyle zdiff3
git config --system push.autoSetupRemote true
git config --system fetch.prune true
git config --system branch.sort -authordate

mkdir -p "$HOME" /etc/profile.d
cat > /etc/profile.d/contain-tools.sh <<'PROFILE'
export PATH="/usr/local/go/bin:/usr/local/zig:$HOME/.bun/bin:$PATH"
PROFILE
. /etc/profile.d/contain-tools.sh

temporary=$(mktemp -d)
trap 'rm -rf "$temporary"' 0
trap 'exit 130' INT
trap 'exit 143' TERM

printf '\nInstalling latest Tap...\n'
curl -fsSL --proto '=https' --proto-redir '=https' --retry 3 --connect-timeout 30 -o "$temporary/install-tap.sh" https://src.ws2.sh/tap/install.sh
sh "$temporary/install-tap.sh"

printf '\nInstalling development tools with Tap...\n'
tap get go pace zig bun:musl builder vet ffwebp:full
ln -sf bun "$HOME/.bun/bin/bunx"

printf '\nVerifying toolchains...\n'
for command_name in tap go gofmt pace bun bunx zig builder vet ffwebp; do
	if ! command -v "$command_name" >/dev/null 2>&1; then
		printf 'error: expected command is missing after provisioning: %s\n' "$command_name" >&2
		exit 1
	fi
done

tap --version
go version
pace version
bun --version
zig version
python3 --version
