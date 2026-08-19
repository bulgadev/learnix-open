#!/bin/bash
set -e

MODEL="${MODEL:-openai/gpt-4o-mini}"
BASE_URL="${BASE_URL:-https://openrouter.ai/api/v1}"
API_KEY="${API_KEY:-}"
PORT="${PORT:-8080}"

if [ -z "$API_KEY" ]; then
  echo "ERROR: Set API_KEY env var or pass -api-key flag"
  echo "Usage: API_KEY=sk-... ./run.sh"
  echo "       ./run.sh -api-key sk-..."
  exit 1
fi

echo "Starting Learnix..."
echo "  Model:   $MODEL"
echo "  Base URL: $BASE_URL"
echo ""

pkill -f 'cloudflared.*tunnel --url' 2>/dev/null || true

go build -o /tmp/learnix .

# API_KEY travels via environment only, never on the command line (ps-visible).
API_KEY="$API_KEY" /tmp/learnix \
  -model "$MODEL" \
  -base-url "$BASE_URL" \
  -port "$PORT" &
APP_PID=$!

sleep 1

echo "Starting Cloudflare tunnel..."
cloudflared --config /dev/null tunnel --url "http://localhost:$PORT" > /tmp/learnix_tunnel.log 2>&1 &
TUNNEL_PID=$!
chmod 600 /tmp/learnix_tunnel.log 2>/dev/null || true

for i in $(seq 1 15); do
  URL=$(grep -oE 'https://[a-z0-9-]+\.trycloudflare\.com' /tmp/learnix_tunnel.log 2>/dev/null | tail -1)
  if [ -n "$URL" ]; then break; fi
  sleep 1
done

if [ -n "$URL" ]; then
  echo ""
  echo "========================================="
  echo "  PUBLIC URL: $URL"
  echo "  Share this with your friend!"
  echo "========================================="
  echo ""
else
  echo "WARNING: Could not detect tunnel URL. Check /tmp/learnix_tunnel.log"
fi

trap "kill $APP_PID $TUNNEL_PID 2>/dev/null; pkill -f 'cloudflared.*tunnel --url' 2>/dev/null" EXIT

echo "Press Ctrl+C to stop everything."
wait $APP_PID
