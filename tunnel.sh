#!/bin/bash
# Start Learnix in Docker (waking OrbStack if needed) and expose it on your own
# domain through a named Cloudflare tunnel. Both keep running after this script
# exits. To stop the tunnel: pkill -f 'cloudflared.*tunnel run <TUNNEL_REF>'
set -e
TUNNEL_NAME="${TUNNEL_NAME:-learnix}"
TUNNEL_REF="${TUNNEL_REF:-f0377d43-0849-4a8e-a789-8d172e6f09f4}"
DOMAIN="${DOMAIN:-learnix.bulga.top}"
TUNNEL_CFG="${TUNNEL_CFG:-$HOME/.cloudflared/${TUNNEL_NAME}.yml}"
TUNNEL_LOG="${TUNNEL_LOG:-/tmp/${TUNNEL_NAME}_tunnel.log}"
TUNNEL_PROTOCOL="${TUNNEL_PROTOCOL:-http2}"
TUNNEL_EDGE_IP_VERSION="${TUNNEL_EDGE_IP_VERSION:-4}"

if ! docker info >/dev/null 2>&1; then
  echo "Docker is sleeping - waking OrbStack..."
  open -a OrbStack
  for _ in $(seq 1 30); do
    sleep 2
    docker info >/dev/null 2>&1 && break
  done
fi

docker compose up -d

pkill -f "cloudflared.*tunnel.*run ${TUNNEL_REF}" 2>/dev/null || true
pkill -f 'cloudflared.*tunnel --url' 2>/dev/null || true

echo "Starting Cloudflare tunnel ${TUNNEL_NAME} for $DOMAIN..."
nohup cloudflared --config "$TUNNEL_CFG" tunnel \
  --protocol "$TUNNEL_PROTOCOL" \
  --edge-ip-version "$TUNNEL_EDGE_IP_VERSION" \
  run "$TUNNEL_REF" > "$TUNNEL_LOG" 2>&1 &
disown
chmod 600 "$TUNNEL_LOG" 2>/dev/null || true

sleep 3
echo ""
echo "========================================="
echo "  PUBLIC URL: https://$DOMAIN"
echo "  Share this with your friend!"
echo "========================================="
echo ""
echo "App and tunnel keep running. Stop tunnel: pkill -f 'cloudflared.*tunnel.*run ${TUNNEL_REF}'"
