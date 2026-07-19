#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
vhs demo/demo.tape
gifski --quality 90 --width 800 --fps 25 -o demo/demo.gif demo/demo.mp4
echo "demo/demo.gif: $(du -h demo/demo.gif | cut -f1)"
