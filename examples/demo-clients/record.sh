#!/usr/bin/env bash
# examples/demo-clients/record.sh — record the demo with asciinema
set -euo pipefail
OUTPUT="${1:-demo-$(date +%Y%m%d-%H%M%S).cast}"
echo "Recording to $OUTPUT"
echo "Press ctrl-d or type 'exit' to stop recording."
asciinema rec "$OUTPUT" --command "bash examples/demo-clients/demo.sh"
echo "Replay with: asciinema play $OUTPUT"
