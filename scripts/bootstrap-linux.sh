#!/bin/sh
# Run on Linux. Downloads only the pinned official tools; no remote EJS loading.
set -eu
test "$(uname -s)" = Linux || {
  echo 'Linux is required.' >&2
  exit 1
}
for tool in curl unzip sha256sum install; do command -v "$tool" >/dev/null 2>&1 || {
  echo "Required tool is missing: $tool" >&2
  exit 1
}; done
dest=${1:-/usr/local/bin}
case "$dest" in /*/bin) ;; *)
  echo "Destination must be an absolute bin directory." >&2
  exit 1
  ;;
esac
mkdir -p "$dest"
case "$(uname -m)" in
x86_64)
  yt=yt-dlp_linux
  sum=dcddc44b6151ccd855a9a3c8fcdb00e897bb872ec9f4908b1b708b16eaaaa114
  deno=x86_64-unknown-linux-gnu
  denosum=e890975bc2549807bbcc0b584ffafda72cc7c93349dd99c80483dd784f6c4609
  ;;
aarch64)
  yt=yt-dlp_linux_aarch64
  sum=d98a0aeb26b670aac94ee892a03a196f3f2cf0a81c3872186199c0c67ab36bde
  deno=aarch64-unknown-linux-gnu
  denosum=d49596c20b9788cf8f5d8ddf644103d4bde56ed239753f0bdb92ba9361d51905
  ;;
*)
  echo 'Only amd64 and arm64 are supported.' >&2
  exit 1
  ;;
esac
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT HUP INT TERM
curl -fsSL "https://github.com/yt-dlp/yt-dlp/releases/download/2026.10.21/$yt.zip" -o "$tmp/yt-dlp.zip"
echo "$sum  $tmp/yt-dlp.zip" | sha256sum -c -
mkdir "$tmp/ytdlp"
unzip -q "$tmp/yt-dlp.zip" -d "$tmp/ytdlp"
curl -fsSL "https://github.com/denoland/deno/releases/download/v2.9.8/deno-$deno.zip" -o "$tmp/deno.zip"
echo "$denosum  $tmp/deno.zip" | sha256sum -c -
unzip -q "$tmp/deno.zip" -d "$tmp"
toolroot=${dest%/bin}/lib/duplex/yt-dlp
mkdir -p "$toolroot"
cp -R "$tmp/ytdlp/." "$toolroot/"
chmod 0755 "$toolroot/$yt"
ln -sf "$toolroot/$yt" "$dest/yt-dlp"
install -m 0755 "$tmp/deno" "$dest/deno"
"$dest/yt-dlp" --version
"$dest/deno" --version
