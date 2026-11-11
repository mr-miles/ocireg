#!/usr/bin/env bash
# Build ocireg for the WD My Cloud EX2 Ultra and wrap it as an installable .bin
# for My Cloud OS5 (upload under Apps > Install an app manually).
#
#   ./package.sh                  -> dist/*_ocireg_*.bin
#   VERSION=0.2.0 ./package.sh    -> same, with that version in the package
#
# VERSION, when set, replaces the Version in package/apkg.rc (only in the
# staged copy, so the source file is left alone). CI sets it from the git tag.
#
# Needs: go, curl, docker. WD's mksapkg-OS5 is a x86-64 binary that wants
# libxml2 and an OpenSSL with Blowfish, so it runs inside a Debian container.
set -euo pipefail
cd "$(dirname "$0")"

MODEL="${MODEL:-MyCloudEX2Ultra}"
# Any Debian-like image works; the tools are installed if the image lacks them.
IMAGE="${IMAGE:-debian:bookworm-slim}"
URL="https://raw.githubusercontent.com/WDCommunity/wdpksrc/refs/heads/master/mksapkg-OS5"
# sha256 of the mksapkg-OS5 this script was written against. If upstream changes
# it, the script stops; check the new file, then update the value.
MKSAPKG_SHA256="${MKSAPKG_SHA256:-62340b1d0eb0433fffa7ceb1a5b6d4886e69b93e6e60297b2f984f5f3557e67a}"

./build.sh

mkdir -p .cache dist
if [ ! -f .cache/mksapkg-OS5 ]; then
    echo "Downloading mksapkg-OS5"
    curl -fsSL -o .cache/mksapkg-OS5 "$URL"
fi
echo "${MKSAPKG_SHA256}  .cache/mksapkg-OS5" | sha256sum -c - || {
    echo "mksapkg-OS5 does not match the expected checksum; delete .cache/ to re-download" >&2
    exit 1
}
chmod +x .cache/mksapkg-OS5

# Stage the package folder: package/ plus the binary.
STAGE="dist/stage"
rm -rf "$STAGE"
mkdir -p "$STAGE/ocireg"
cp -r package/. "$STAGE/ocireg/"
cp ocireg-armv7 "$STAGE/ocireg/ocireg"
chmod +x "$STAGE/ocireg/ocireg"

if [ -n "${VERSION:-}" ]; then
    [[ "$VERSION" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]] || {
        echo "VERSION must look like 1.2.3 (got '$VERSION')" >&2
        exit 1
    }
    sed -i "s/^Version:[[:space:]].*/Version:\t$VERSION/" "$STAGE/ocireg/apkg.rc"
    grep -q "^Version:	$VERSION\$" "$STAGE/ocireg/apkg.rc" || {
        echo "could not set the version in apkg.rc" >&2
        exit 1
    }
fi
echo "Packaging version $(sed -n 's/^Version:[[:space:]]*//p' "$STAGE/ocireg/apkg.rc")"

# Pass proxy settings through when set (a no-op otherwise).
docker run --rm -e http_proxy -e https_proxy -e HTTP_PROXY -e HTTPS_PROXY -e no_proxy -e NO_PROXY \
    -e HOST_UID="$(id -u)" -e HOST_GID="$(id -g)" -e MODEL="$MODEL" \
    -v "$PWD/.cache/mksapkg-OS5:/usr/local/bin/mksapkg-OS5:ro" \
    -v "$PWD/$STAGE:/work" \
    "$IMAGE" bash -euc '
        if ! command -v openssl >/dev/null || ! ldconfig -p | grep -q libxml2.so.2; then
            apt-get update -qq >/dev/null
            apt-get install -y -qq --no-install-recommends openssl libxml2 >/dev/null
        fi
        # mksapkg signs with Blowfish, which OpenSSL 3 only offers in its legacy provider.
        cat > /tmp/openssl.cnf <<CNF
openssl_conf = init
[init]
providers = providers
[providers]
default = default_sect
legacy = legacy_sect
[default_sect]
activate = 1
[legacy_sect]
activate = 1
CNF
        export OPENSSL_CONF=/tmp/openssl.cnf
        cd /work/ocireg
        mksapkg-OS5 -E -s -m "$MODEL"
        chown -R "$HOST_UID:$HOST_GID" /work
    '

# mksapkg writes the .bin next to the package folder, sometimes with a suffix.
shopt -s nullglob
rm -f dist/*.bin
for f in "$STAGE"/*.bin*; do
    base="$(basename "${f%.bin*}").bin"
    mv "$f" "dist/$base"
    echo "Built dist/$base"
done
[ -n "$(ls dist/*.bin 2>/dev/null)" ] || { echo "no .bin was produced" >&2; exit 1; }
rm -rf "$STAGE"
