#!/bin/sh
set -eu

root=$(cd "$(dirname "$0")/../.." && pwd)
cd "$root"

arch=$(dpkg --print-architecture)
if [ -z "${VERSION:-}" ]; then
	VERSION="0.0.0~git$(git log -1 --format=%cd --date=format:%Y%m%d).$(git rev-parse --short HEAD)"
	if [ -n "$(git status --porcelain)" ]; then
		VERSION="$VERSION+dirty"
	fi
fi

stage="$root/bin/deb/gty_${VERSION}_${arch}"
rm -rf "$stage"
mkdir -p "$stage/DEBIAN"

install -Dm755 "$root/bin/gty" "$stage/usr/bin/gty"
install -Dm644 packaging/deb/gty.desktop "$stage/usr/share/applications/gty.desktop"
install -Dm644 config.example.toml "$stage/usr/share/doc/gty/config.example.toml"
install -Dm644 -t "$stage/usr/share/doc/gty/themes" themes/*.toml
{
	printf 'gty bundles fonts compiled into the binary.\n\n'
	printf '== JetBrains Mono Nerd Font: SIL Open Font License 1.1 ==\n\n'
	cat assets/OFL.txt
	printf '\n\n== DejaVu Sans Mono ==\n\n'
	cat assets/LICENSE-DejaVu.txt
} >"$stage/usr/share/doc/gty/copyright"
chmod 644 "$stage/usr/share/doc/gty/copyright"

glibc=$(objdump -T "$stage/usr/bin/gty" | grep -o 'GLIBC_[0-9.]*' | sort -uV | tail -1 | cut -d_ -f2)
size=$(du -sk "$stage/usr" | cut -f1)

cat >"$stage/DEBIAN/control" <<CONTROL
Package: gty
Version: $VERSION
Architecture: $arch
Maintainer: $(git config user.name) <$(git config user.email)>
Installed-Size: $size
Section: x11
Priority: optional
Depends: libc6 (>= $glibc), libgcc-s1, libvulkan1, libxkbcommon0, libwayland-client0, libwayland-cursor0, libwayland-egl1, libx11-6, libx11-xcb1, libxcursor1, libxrandr2, libxinerama1, libxi6, libxext6
Recommends: libdecor-0-0, mesa-vulkan-drivers
Description: GPU-accelerated terminal emulator
 gty renders through WebGPU and runs on Wayland and X11.
CONTROL

chmod -R go-w "$stage"
dpkg-deb --root-owner-group --build "$stage" "$root/bin/gty_${VERSION}_${arch}.deb"
