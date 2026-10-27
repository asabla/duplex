#!/bin/sh
# Bound downloads on the Ubuntu 24.04 runner without interrupting dpkg.
set -eu

test "$(lsb_release -cs)" = noble
sources=$(mktemp)
trap 'rm -f "$sources"' EXIT HUP INT TERM
chmod 0644 "$sources"

download_packages() {
  timeout 120s sudo apt-get -o Acquire::Retries=0 "$@" update &&
    timeout 120s sudo apt-get -o Acquire::Retries=0 "$@" \
      install --download-only --no-install-recommends -y fuse3 ffmpeg ripgrep
}

if download_packages; then
  sudo apt-get install --no-download --no-install-recommends -y fuse3 ffmpeg ripgrep
else
  printf '%s\n' 'Default package download failed or timed out; use the official Ubuntu archives.' >&2
  cat >"$sources" <<'EOF'
deb [signed-by=/usr/share/keyrings/ubuntu-archive-keyring.gpg] https://archive.ubuntu.com/ubuntu noble main universe
deb [signed-by=/usr/share/keyrings/ubuntu-archive-keyring.gpg] https://archive.ubuntu.com/ubuntu noble-updates main universe
deb [signed-by=/usr/share/keyrings/ubuntu-archive-keyring.gpg] https://security.ubuntu.com/ubuntu noble-security main universe
EOF
  set -- -o "Dir::Etc::sourcelist=$sources" -o Dir::Etc::sourceparts=-
  download_packages "$@"
  sudo apt-get "$@" install --no-download --no-install-recommends -y fuse3 ffmpeg ripgrep
fi
