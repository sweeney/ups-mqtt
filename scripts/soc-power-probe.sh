#!/bin/bash
#
# soc-power-probe.sh — sanity-check the UPS load_watts reading against what the
# host Apple Silicon Mac is actually drawing.
#
# Apple Silicon exposes on-chip power via `powermetrics` (root only). This reports
# the SoC package (CPU+GPU+ANE) — NOT total wall power — so it is a lower bound:
# the whole Mac mini at the wall is typically a few watts above the SoC figure.
# Useful when the UPS reports 0 W and you want to confirm the load is merely below
# the UPS meter's resolution rather than actually absent. See the "load_watts
# accuracy at low load" note in README.md.
#
# Usage:   sudo bash scripts/soc-power-probe.sh [SECONDS]
# Example: sudo bash scripts/soc-power-probe.sh 60
#
# Parses with BSD awk (the macOS default) — no GNU-awk extensions.

set -euo pipefail

SAMPLES="${1:-60}"     # seconds (1 sample/sec)
INTERVAL_MS=1000
RAW="$(mktemp /tmp/soc-power-probe.XXXXXX)"
trap 'rm -f "$RAW"' EXIT

if [[ "$(uname -m)" != "arm64" ]]; then
  echo "This probe relies on Apple Silicon powermetrics; host is not arm64." >&2
  exit 1
fi
if [[ "$(id -u)" != "0" ]]; then
  echo "powermetrics needs root — run with sudo." >&2
  exit 1
fi

echo "Sampling SoC power for ~${SAMPLES}s..." >&2

# One "Combined Power (CPU + GPU + ANE): N mW" line per second.
powermetrics -i "$INTERVAL_MS" -n "$SAMPLES" --samplers cpu_power 2>/dev/null \
  | grep -iE "Combined Power" > "$RAW" || true

# BSD-awk-safe: the mW value is the second-to-last field ("... : N mW").
awk '
  {
    w = $(NF-1) / 1000.0
    sum += w; n++
    if (n == 1 || w < min) min = w
    if (n == 1 || w > max) max = w
  }
  END {
    if (n == 0) { print "No samples captured (is powermetrics available?)."; exit 1 }
    printf "SoC package power over %d samples:\n", n
    printf "  avg = %.3f W\n  min = %.3f W\n  max = %.3f W\n", sum/n, min, max
    print  "Note: SoC only — whole-machine wall draw is a few watts higher."
  }
' "$RAW"
