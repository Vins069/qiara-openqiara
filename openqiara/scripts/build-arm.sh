#!/bin/sh
# Builds openqiarad for the camera (ARMv7 hard-float, CGO through zig cc) with a reproducible checksum:
# -buildvcs=false (otherwise Go stamps the git state into the binary and the checksum changes with every
# commit or dirty tree), -trimpath, and a date passed in rather than read from the clock.
#   sh scripts/build-arm.sh v16 2026-11-01 [output] [seq]   (output relative to the current directory,
#                                                       default ./openqiarad-claude-<version>; seq: number of the
#                                                       signed update this binary is, the "seq" of its update.json,
#                                                       0 = unsigned build, see docs/mise-a-jour-signee.md)
# The version string is v0.1.0-rc.23-claude-<version>: OpenQIARA Claude V16 is built on the
# OpenQiara v0.1.0-rc.23 release; the « -claude » suffix blocks web UI and automatic updates
# (handlers_update.go, main.go), which would replace the distribution by a published release.
# Requires Go 1.26.11 (the go.mod toolchain line: GOTOOLCHAIN=auto downloads it; 1.26.1 has known flaws in
# crypto/x509, crypto/tls and net/http reachable by the signed update's HTTPS client) and zig 0.17.0
# (pip install ziglang==0.17.0). Both are checked: another version gives another checksum. OQ_TOOLCHAIN_LIBRE=1
# skips the check (development builds only, never a delivery).
set -e
TAG=${1:?version (e.g. v16)}; DATE=${2:?date (YYYY-MM-DD)}; OUT=${3:-openqiarad-claude-$TAG}; SEQ=${4:-0}
case "$SEQ" in ''|*[!0-9]*) echo "seq : entier positif attendu" >&2; exit 1;; esac
case "$OUT" in /*) ;; *) OUT=$(pwd)/$OUT;; esac
cd "$(dirname "$0")/.."
if [ "${OQ_TOOLCHAIN_LIBRE:-0}" != 1 ]; then
  GOV=$(GOTOOLCHAIN=${GOTOOLCHAIN:-auto} go env GOVERSION)
  [ "$GOV" = go1.26.11 ] || { echo "Go 1.26.11 attendu, trouvé : $GOV (GOTOOLCHAIN=auto le télécharge)" >&2; exit 1; }
  ZV=$(python3 -m ziglang version 2>/dev/null || true)
  [ "$ZV" = 0.17.0 ] || { echo "zig 0.17.0 attendu, trouvé : ${ZV:-absent} (pip install ziglang==0.17.0)" >&2; exit 1; }
fi
ZIGDIR=$(mktemp -d "${TMPDIR:-/tmp}/zigcc.XXXXXX"); trap 'rm -rf "$ZIGDIR"' EXIT INT TERM
printf '#!/bin/sh\nexec python3 -m ziglang cc -target arm-linux-gnueabihf.2.30 "$@"\n' > "$ZIGDIR/zigcc"; chmod +x "$ZIGDIR/zigcc"
GOTOOLCHAIN=${GOTOOLCHAIN:-auto} CGO_ENABLED=1 CC="$ZIGDIR/zigcc" GOOS=linux GOARCH=arm GOARM=7 \
  go build -buildvcs=false -trimpath -ldflags "-s -w -X main.version=v0.1.0-rc.23-claude-$TAG -X main.commit=claude-$TAG -X main.date=$DATE -X 'main.signedSeq=OQSEQ=$SEQ;'" -o "$OUT" ./cmd/openqiarad
echo "$(sha256sum "$OUT")  ($(stat -c %s "$OUT") bytes, Go $(go version | cut -d' ' -f3), zig $(python3 -m ziglang version))"
