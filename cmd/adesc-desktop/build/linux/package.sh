#!/usr/bin/env bash
# Empaqueta build/bin/adesc-desktop (Linux) como .deb y AppImage.
#
#   build/linux/package.sh <versión> [arquitectura-debian]
#
# Ambos paquetes dependen de GTK 3 y WebKitGTK 4.1 del sistema (no se
# incluyen): .deb con Depends y AppImage con una AppDir mínima.
# Necesita dpkg-deb; para el AppImage descarga appimagetool si no está en
# el PATH (APPIMAGETOOL=/ruta lo evita).
set -euo pipefail

HERE=$(cd "$(dirname "$0")" && pwd)
ROOT=$(cd "$HERE/../.." && pwd)          # cmd/adesc-desktop
BIN="$ROOT/build/bin/adesc-desktop"
VERSION="${1:?uso: package.sh <versión> [arch]}"
ARCH="${2:-amd64}"
OUT="$ROOT/build/dist"
mkdir -p "$OUT"
[ -x "$BIN" ] || { echo "falta $BIN (ejecuta wails build primero)" >&2; exit 1; }

case "$ARCH" in
  amd64) AI_ARCH=x86_64 ;;
  arm64) AI_ARCH=aarch64 ;;
  *) AI_ARCH="$ARCH" ;;
esac

# --- .deb ---------------------------------------------------------------
PKG=$(mktemp -d)
chmod 755 "$PKG"
trap 'rm -rf "$PKG" "${APPDIR:-}"' EXIT
install -Dm755 "$BIN" "$PKG/usr/bin/adesc-desktop"
install -Dm644 "$HERE/admindescargas.desktop" "$PKG/usr/share/applications/admindescargas.desktop"
install -Dm644 "$ROOT/build/appicon.png" "$PKG/usr/share/pixmaps/admindescargas.png"
mkdir -p "$PKG/DEBIAN"
cat > "$PKG/DEBIAN/control" <<CONTROL
Package: admindescargas
Version: ${VERSION}
Section: net
Priority: optional
Architecture: ${ARCH}
Depends: libgtk-3-0 | libgtk-3-0t64, libwebkit2gtk-4.1-0
Recommends: wl-clipboard
Maintainer: AdminDescargas <noreply@admindescargas.invalid>
Description: Gestor de descargas multi-hoster
 App de escritorio de AdminDescargas: descargas de MEGA, MediaFire,
 Google Drive, TeraBox y otros, con rotación de IP, bandeja del sistema y
 monitor del portapapeles.
CONTROL
cat > "$PKG/DEBIAN/postinst" <<'POSTINST'
#!/bin/sh
set -e
command -v update-desktop-database >/dev/null 2>&1 && update-desktop-database -q /usr/share/applications || true
POSTINST
chmod 755 "$PKG/DEBIAN/postinst"
dpkg-deb --root-owner-group --build "$PKG" "$OUT/admindescargas_${VERSION}_${ARCH}.deb"

# --- AppImage -----------------------------------------------------------
APPDIR=$(mktemp -d)/AdminDescargas.AppDir
mkdir -p "$APPDIR/usr/bin"
install -m755 "$BIN" "$APPDIR/usr/bin/adesc-desktop"
install -m644 "$HERE/admindescargas.desktop" "$APPDIR/admindescargas.desktop"
install -m644 "$ROOT/build/appicon.png" "$APPDIR/admindescargas.png"
cat > "$APPDIR/AppRun" <<'APPRUN'
#!/bin/sh
HERE=$(dirname "$(readlink -f "$0")")
exec "$HERE/usr/bin/adesc-desktop" "$@"
APPRUN
chmod 755 "$APPDIR/AppRun"

TOOL="${APPIMAGETOOL:-$(command -v appimagetool || true)}"
if [ -z "$TOOL" ]; then
  TOOL="$OUT/.appimagetool-${AI_ARCH}.AppImage"
  if [ ! -x "$TOOL" ]; then
    curl -fsSL -o "$TOOL" "https://github.com/AppImage/appimagetool/releases/download/continuous/appimagetool-${AI_ARCH}.AppImage"
    chmod +x "$TOOL"
  fi
fi
# Without FUSE (CI, containers) appimagetool runs extracted.
APPIMAGE_EXTRACT_AND_RUN=1 ARCH="$AI_ARCH" "$TOOL" --no-appstream "$APPDIR" "$OUT/AdminDescargas-${VERSION}-${AI_ARCH}.AppImage"
echo "Paquetes en $OUT:"
ls -l "$OUT"
