#!/bin/bash
set -euo pipefail

ROOT="$(cd "$(dirname "$0")" && pwd)"
cd "$ROOT"

VERSION="1.1.1"
BUILD="0001"
PKG_VERSION="${VERSION}-${BUILD}"
DIST="$ROOT/dist"
BUILD_DIR="$ROOT/build"

command -v go >/dev/null || { echo "go fehlt" >&2; exit 1; }
command -v tar >/dev/null || { echo "tar fehlt" >&2; exit 1; }

gofmt -w ./*.go
go vet ./...
rm -rf "$BUILD_DIR" "$DIST"
mkdir -p "$BUILD_DIR" "$DIST"

# arch:GOARCH:GOARM
ARCHS=(
  "x86_64:amd64:"
  "armv8:arm64:"
  "armv7:arm:7"
  "armada38x:arm:7"
)

for spec in "${ARCHS[@]}"; do
  IFS=: read -r spk_arch go_arch go_arm <<<"$spec"
  echo "==> $spk_arch ($go_arch${go_arm:+/GOARM=$go_arm})"
  bin="$BUILD_DIR/github-backup-$spk_arch"
  if [ -n "$go_arm" ]; then
    CGO_ENABLED=0 GOOS=linux GOARCH="$go_arch" GOARM="$go_arm" \
      go build -trimpath -ldflags="-s -w" -o "$bin" .
  else
    CGO_ENABLED=0 GOOS=linux GOARCH="$go_arch" \
      go build -trimpath -ldflags="-s -w" -o "$bin" .
  fi
  chmod 0755 "$bin"

  stage="$BUILD_DIR/stage-$spk_arch"
  rm -rf "$stage"
  mkdir -p "$stage/package/bin" "$stage/package/ui"
  cp "$bin" "$stage/package/bin/github-backup"
  chmod 0755 "$stage/package/bin/github-backup"
  cp "$ROOT/web/index.html" "$stage/package/ui/index.html"
  cp "$ROOT/web/app.css" "$stage/package/ui/app.css"
  cp "$ROOT/web/app.js" "$stage/package/ui/app.js"
  cp -a "$ROOT/ui/." "$stage/package/ui/"
  chmod 0755 "$stage/package/ui/api.cgi"

  ( cd "$stage/package" && tar -czf "$stage/package.tgz" bin ui )

  spk="$BUILD_DIR/spk-$spk_arch"
  mkdir -p "$spk/conf" "$spk/scripts" "$spk/WIZARD_UIFILES"
  cp "$stage/package.tgz" "$spk/package.tgz"
  cp "$ROOT/spk/conf/privilege" "$spk/conf/privilege"
  cp "$ROOT/spk/conf/resource" "$spk/conf/resource"
  cp "$ROOT/spk/scripts/preinst" "$spk/scripts/preinst"
  cp "$ROOT/spk/scripts/postinst" "$spk/scripts/postinst"
  cp "$ROOT/spk/scripts/preuninst" "$spk/scripts/preuninst"
  cp "$ROOT/spk/scripts/postuninst" "$spk/scripts/postuninst"
  cp "$ROOT/spk/scripts/start-stop-status" "$spk/scripts/start-stop-status"
  cp "$ROOT/spk/WIZARD_UIFILES/install_uifile_enu" "$spk/WIZARD_UIFILES/install_uifile"
  cp "$ROOT/spk/WIZARD_UIFILES/install_uifile_enu" "$spk/WIZARD_UIFILES/install_uifile_enu"
  cp "$ROOT/spk/WIZARD_UIFILES/install_uifile_ger" "$spk/WIZARD_UIFILES/install_uifile_ger"
  cp "$ROOT/spk/LICENSE" "$spk/LICENSE"
  cp "$ROOT/PACKAGE_ICON.PNG" "$spk/PACKAGE_ICON.PNG"
  cp "$ROOT/PACKAGE_ICON_256.PNG" "$spk/PACKAGE_ICON_256.PNG"
  chmod 0755 "$spk/scripts/preinst" "$spk/scripts/postinst" \
    "$spk/scripts/preuninst" "$spk/scripts/postuninst" "$spk/scripts/start-stop-status"

  checksum=$(md5sum "$spk/package.tgz" | awk '{print $1}')

  cat >"$spk/INFO" <<EOF
package="github-backup"
version="$PKG_VERSION"
arch="$spk_arch"
os_min_ver="7.0-40000"
description="Mirrors every GitHub repository the token can access as bare Git mirrors."
description_ger="Spiegelt alle GitHub-Repositories des Tokens als bare Git-Mirrors."
displayname="GitHub Backup"
displayname_ger="GitHub Backup"
maintainer="evait security GmbH"
maintainer_url="https://evait.de"
distributor="evait security GmbH"
distributor_url="https://evait.de"
ctl_stop="yes"
reloadui="yes"
dsmuidir="ui"
dsmappname="com.evait.githubbackup"
checksum="$checksum"
EOF

  out="$DIST/github-backup-${VERSION}-${spk_arch}.spk"
  ( cd "$spk" && tar --format=ustar -cf "$out" INFO LICENSE package.tgz scripts conf WIZARD_UIFILES \
      PACKAGE_ICON.PNG PACKAGE_ICON_256.PNG )
  echo "    -> $out"
done

cp "$ROOT/README.md" "$DIST/README.md"
( cd "$DIST" && sha256sum ./*.spk > SHA256SUMS )
if command -v zip >/dev/null 2>&1; then
  ( cd "$DIST" && zip -q "github-backup-${VERSION}-all.zip" ./*.spk README.md SHA256SUMS )
fi
ls -lh "$DIST"
