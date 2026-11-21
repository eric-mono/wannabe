#!/usr/bin/env bash
# happy-sandbox installer: the tool, its exit client (mihomo) and the image,
# from the happy-dist repository. Everything goes to ~/.happy-sandbox
# (bin/ for the tool; your sandboxes next to it), plus a link in ~/.local/bin.
#
#   curl -fsSL -u USER:TOKEN <base>/sandbox/install.sh | HAPPY_DIST_AUTH=USER:TOKEN bash
#   ~/.happy-sandbox/bin/install.sh              # update to the latest release
#   ~/.happy-sandbox/bin/install.sh --uninstall  # remove the tool and the image
#
# It checks the host first (happy-sandbox check) and stops with the reasons
# when a sandbox cannot run here. Nothing needs root; it never uses sudo.
set -euo pipefail

# Where releases come from: HAPPY_DIST_URL (an http(s) URL or file:///dir),
# else the release directory this script sits in, else the one happy was
# installed from (~/.happy-dist/url), else the default.
DEFAULT_DIST_URL=""
# Run from a release directory itself (bash ~/dist/sandbox/install.sh): its parent is the source.
HERE=""
if [ -f "${BASH_SOURCE[0]:-}" ]; then
    HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
    { [ -f "$HERE/latest" ] && [ -d "$HERE/releases" ]; } || HERE=""
fi
if [ -n "${HAPPY_DIST_URL:-}" ]; then DIST="${HAPPY_DIST_URL%/}"
elif [ -n "$HERE" ]; then DIST="file://$(dirname "$HERE")"
elif [ -s "${HAPPY_DIST_HOME:-$HOME/.happy-dist}/url" ]; then DIST="$(head -n1 "${HAPPY_DIST_HOME:-$HOME/.happy-dist}/url")"
else DIST="$DEFAULT_DIST_URL"; fi
[ -n "$DIST" ] || { printf 'error: no release source: set HAPPY_DIST_URL to where your build is published (https://… or file:///path/to/dist)\n' >&2; exit 1; }
BASE="$DIST/sandbox"
# Large files in a Gogs repository (…/raw/…) are Git LFS objects: the raw
# URLs give only their pointers, so they are fetched by sha256 (the
# manifest's checksums). Any other source serves them by path.
case "$DIST" in */raw/*) LFS_DEFAULT="${DIST%/raw/*}.git/info/lfs/objects/basic" ;; *) LFS_DEFAULT="" ;; esac
LFS="${HAPPY_DIST_LFS-$LFS_DEFAULT}"
HOME_DIR="${HSB_HOME:-$HOME/.happy-sandbox}"
BIN="$HOME_DIR/bin"
LINK_DIR="$HOME/.local/bin"
PODMAN="${HSB_PODMAN:-podman}"
IMAGE="localhost/happy-sandbox:latest"

say()  { printf '\033[1m==>\033[0m %s\n' "$*"; }
warn() { printf '\033[33mwarning:\033[0m %s\n' "$*" >&2; }
die()  { printf '\033[31merror:\033[0m %s\n' "$*" >&2; exit 1; }

[ "$(id -u)" != 0 ] || die "run this as your own user, not root: the sandbox relies on rootless podman"
[ "$(uname -s)" = Linux ] || die "happy-sandbox runs on Linux only (this is $(uname -s))"
case "$(uname -m)" in
    x86_64|amd64) ARCH=amd64 ;;
    aarch64|arm64) die "arm64 is not supported yet (the image and its tools are built for amd64)" ;;
    *) die "unsupported CPU: $(uname -m)" ;;
esac
command -v curl >/dev/null || die "curl is required (sudo apt install curl)"
command -v sha256sum >/dev/null || die "sha256sum is required (coreutils)"

if [ "${1:-}" = --uninstall ]; then
    left="$(find "$HOME_DIR" -mindepth 2 -maxdepth 2 -name config.json 2>/dev/null | wc -l)"
    if [ "$left" -gt 0 ]; then
        die "there are still $left sandbox(es) in $HOME_DIR: happy-sandbox list, then happy-sandbox destroy N --yes for each"
    fi
    "$PODMAN" rmi "$IMAGE" >/dev/null 2>&1 && say "removed the image $IMAGE" || true
    rm -f "$LINK_DIR/happy-sandbox"
    rm -rf "$BIN"
    rmdir "$HOME_DIR" 2>/dev/null || true
    say "happy-sandbox removed"
    exit 0
fi

mkdir -p "$BIN"
chmod 700 "$HOME_DIR"

# Login for the dist repository: given now, saved earlier, or happy's.
AUTH="${HAPPY_DIST_AUTH:-}"
if [ -n "$AUTH" ]; then
    (umask 077; printf '%s\n' "$AUTH" > "$BIN/dist-auth")
elif [ -f "$BIN/dist-auth" ]; then
    AUTH="$(head -n1 "$BIN/dist-auth")"
elif [ -f "$HOME/.happy-dist/auth" ]; then
    AUTH="$(head -n1 "$HOME/.happy-dist/auth")"
fi

# get URL DEST [progress]: exactly HTTP 200 or fail (Gogs answers 302 to
# its login page when the credentials are missing or wrong).
get() {
    local code opts=(-sS -L)
    if [ "${3:-}" = progress ] && [ -t 2 ]; then opts=(-S -L); fi
    if [ -n "$AUTH" ]; then opts+=(-u "$AUTH"); fi
    code="$(curl "${opts[@]}" -o "$2" -w '%{http_code}' "$1")" || die "download failed: $1"
    case "$code" in
        200) ;;
        000) case "$1" in file://*) ;; *) die "$1: no HTTP answer" ;; esac ;;
        302|401|403) die "$1: HTTP $code — the dist repository needs a login; set HAPPY_DIST_AUTH=USER:TOKEN" ;;
        *) die "$1: HTTP $code" ;;
    esac
}
fetch() { get "$BASE/$1" "$2" "${3:-}"; }          # a text file of the release
# fetch_big PATH SHA256 DEST [progress]: a large file, by sha256 from LFS or by path.
fetch_big() { if [ -n "$LFS" ]; then get "$LFS/$2" "$3" "${4:-}"; else fetch "$1" "$3" "${4:-}"; fi; }

# field KEY: a value of the release manifest ("key value" lines).
field() { awk -v k="$1" '$1 == k { print $2; exit }' "$work/manifest"; }

verify() { # verify FILE SHA256
    [ "$(sha256sum "$1" | cut -d' ' -f1)" = "$2" ] || die "checksum mismatch for $(basename "$1") (download broken or tampered with)"
}

# Downloads next to the sandboxes: the image needs ~3 GB while loading, more
# than many /tmp have.
work="$(mktemp -d "$HOME_DIR/.install.XXXXXX")"
trap 'rm -rf "$work"' EXIT

fetch latest "$work/latest"
release="$(head -n1 "$work/latest" | tr -d '[:space:]')"
[ -n "$release" ] || die "no release published"
fetch "releases/$release/manifest" "$work/manifest"
say "happy-sandbox $release"

# 1. The tool and mihomo: verified, then swapped in whole.
for f in happy-sandbox mihomo; do
    fetch_big "releases/$release/$(field "$f")" "$(field "$f.sha256")" "$work/$f" progress
    verify "$work/$f" "$(field "$f.sha256")"
    chmod 755 "$work/$f"
done
fetch install.sh "$work/install.sh"
chmod 755 "$work/install.sh"
for f in happy-sandbox mihomo install.sh; do mv -f "$work/$f" "$BIN/$f"; done
mkdir -p "$LINK_DIR"
ln -sf "$BIN/happy-sandbox" "$LINK_DIR/happy-sandbox"
say "installed $BIN/happy-sandbox ($("$BIN/happy-sandbox" version | head -n1))"

# 2. Can this host run sandboxes? Stop here with the reasons if not (before
#    the large image download).
say "checking this host"
if ! HSB_HOME="$HOME_DIR" HSB_PODMAN="$PODMAN" "$BIN/happy-sandbox" check; then
    die "this host cannot run happy-sandbox yet: fix the FAIL lines above, then run $BIN/install.sh again"
fi

# 3. The image, unless this exact one is installed.
want="$(field image_id)"
have="$("$PODMAN" image inspect --format '{{.Id}}' "$IMAGE" 2>/dev/null || true)"
if [ "$have" = "$want" ]; then
    say "image up to date (${want:0:12})"
else
    say "downloading the image ($(field image_size_mb) MB compressed)"
    fetch_big "images/$(field image)" "$(field image.sha256)" "$work/image.tar.gz" progress
    say "verifying"
    verify "$work/image.tar.gz" "$(field image.sha256)"
    say "loading the image into podman (a few minutes)"
    gzip -dc "$work/image.tar.gz" > "$work/image.tar"
    rm -f "$work/image.tar.gz"
    HSB_PODMAN="$PODMAN" "$BIN/happy-sandbox" image load "$work/image.tar"
    have="$("$PODMAN" image inspect --format '{{.Id}}' "$IMAGE" 2>/dev/null || true)"
    [ "$have" = "$want" ] || die "the loaded image is not the release's (${have:0:12} ≠ ${want:0:12})"
    if [ -n "$(find "$HOME_DIR" -mindepth 2 -maxdepth 2 -name config.json 2>/dev/null)" ]; then
        say "existing sandboxes move to the new image at their next down / up (home kept)"
    fi
fi

case ":$PATH:" in
    *":$LINK_DIR:"*) ;;
    *) warn "$LINK_DIR is not in your PATH: add it (e.g. in ~/.bashrc: export PATH=\"\$HOME/.local/bin:\$PATH\")" ;;
esac

cat <<EOF

happy-sandbox $release is installed. Next:

  happy-sandbox init --node NODE.yaml --exit-ip EXIT_IP \\
      [--timezone America/Los_Angeles --locale en_US.UTF-8] [--share ~/proj:/workspace/proj]
      # it starts with the host (--manual: only by hand)
  happy-sandbox up
  happy-sandbox shell          # then, once: happy auth login, claude, codex login

  NODE.yaml: one mihomo proxy (VLESS etc.), chmod 600; EXIT_IP: the public IP it exits from.
  Update: $BIN/install.sh    Remove: $BIN/install.sh --uninstall
EOF
