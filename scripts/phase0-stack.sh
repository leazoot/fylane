#!/bin/sh
# Local end-to-end stack: relay in OAuth mode + companion + a Cloudflare quick
# tunnel exposing the relay. The public URL changes on every restart, which is
# why the companion is paired against it after the tunnel is up rather than
# before. Requires a proxy direct rule for argotunnel.com behind a fake-IP
# proxy.
#
# Usage: scripts/phase0-stack.sh [workspace-dir]   (Ctrl-C stops everything)
#
# For a companion that publishes itself with no relay at all, use direct mode
# instead: `fylane-companion direct` plus the Connection screen (docs/20).
set -eu
cd "$(dirname "$0")/.."

WORKSPACE="${1:-tmp/e2e-workspace}"
RELAY_ADDR="127.0.0.1:9000"

mkdir -p tmp/bin tmp/state "$WORKSPACE"

go build -o tmp/bin/fylane-relay ./relay/cmd/relay
go build -o tmp/bin/fylane-companion ./companion/cmd/companion

# The quick tunnel names the relay, and the relay has to advertise that same
# name as its issuer, so cloudflared starts first.
rm -f tmp/state/cf.log
cloudflared tunnel --url "http://$RELAY_ADDR" --protocol http2 --no-autoupdate \
  > tmp/state/cf.log 2>&1 &
CF_PID=$!
trap 'kill $CF_PID ${RELAY_PID:-} ${COMPANION_PID:-} 2>/dev/null || true' EXIT INT TERM

echo "waiting for the quick tunnel to name itself..."
ISSUER=""
i=0
while [ $i -lt 60 ]; do
  ISSUER=$(grep -o 'https://[a-z0-9-]*\.trycloudflare\.com' tmp/state/cf.log | head -1 || true)
  [ -n "$ISSUER" ] && break
  i=$((i + 1))
  sleep 1
done
if [ -z "$ISSUER" ]; then
  echo "cloudflared did not print a URL; see tmp/state/cf.log" >&2
  exit 1
fi
echo "relay will be reachable at $ISSUER"

# Metadata persists in a SQLite file, so restarting the relay does not throw
# away the device registration below.
FYLANE_RELAY_DB="$PWD/tmp/state/relay.db"
export FYLANE_RELAY_DB
./tmp/bin/fylane-relay serve -addr "$RELAY_ADDR" -issuer "$ISSUER" &
RELAY_PID=$!
sleep 1

# Registers the device (credentials go to the OS keychain), stores the tunnel
# endpoint, and prints the pairing code to type on the platform's page.
./tmp/bin/fylane-companion pair -relay "$ISSUER"

./tmp/bin/fylane-companion serve -workspace "$WORKSPACE" -probe &
COMPANION_PID=$!

echo
echo "connector URL for the platform: $ISSUER/mcp"
echo "another pairing code: ./tmp/bin/fylane-companion pair -relay $ISSUER"
echo
wait $COMPANION_PID
