#!/bin/sh
# Installs the latest quest-map release for Linux (amd64, arm64) into ~/.local/bin.
#   curl -LsSf https://github.com/RoyalNoob/quest-map/releases/latest/download/quest-map-installer.sh | sh
# QUEST_MAP_INSTALL_DIR overrides the folder; QUEST_MAP_VERSION (e.g. v1.0.0) picks a release.
set -eu

repo="RoyalNoob/quest-map"
dir="${QUEST_MAP_INSTALL_DIR:-$HOME/.local/bin}"

case "$(uname -s)" in
  Linux) os=linux ;;
  *) echo "quest-map installer: $(uname -s) is not supported (Linux and Windows only)" >&2; exit 1 ;;
esac
case "$(uname -m)" in
  x86_64 | amd64) arch=amd64 ;;
  aarch64 | arm64) arch=arm64 ;;
  *) echo "quest-map installer: $(uname -m) is not supported (amd64 and arm64 only)" >&2; exit 1 ;;
esac

if [ -n "${QUEST_MAP_VERSION:-}" ]; then
  base="https://github.com/$repo/releases/download/$QUEST_MAP_VERSION"
else
  base="https://github.com/$repo/releases/latest/download"
fi
name="quest-map_${os}_${arch}.tar.gz"
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

echo "downloading $name"
curl -fsSL -o "$tmp/$name" "$base/$name"
curl -fsSL -o "$tmp/checksums.txt" "$base/checksums.txt"
(cd "$tmp" && grep "  $name\$" checksums.txt | sha256sum -c - >/dev/null) || {
  echo "quest-map installer: $name does not match checksums.txt; nothing was installed" >&2
  exit 1
}
mkdir -p "$dir"
# Unpack next to the target and rename over it: a rename on one filesystem leaves a running quest-map its open
# file, where mv from another filesystem would write into it ("Text file busy").
new="$(mktemp -d "$dir/.quest-map-XXXXXX")"
trap 'rm -rf "$tmp" "$new"' EXIT
tar -xzf "$tmp/$name" -C "$new" quest-map
chmod 755 "$new/quest-map"
mv -f "$new/quest-map" "$dir/quest-map"

echo "installed $("$dir/quest-map" version) to $dir/quest-map"
case ":$PATH:" in
  *":$dir:"*) ;;
  *) echo "add $dir to your PATH, e.g.: echo 'export PATH=\"$dir:\$PATH\"' >> ~/.profile" ;;
esac
echo "next: quest-map install claude-code   (registers quest-map with Claude Code)"
