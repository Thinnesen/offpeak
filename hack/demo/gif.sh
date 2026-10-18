#!/usr/bin/env bash
# Converts the recorded MP4 demos to compact GIFs for the README.
# It runs after hack/demo/record.sh. Delete the demo cluster first if memory is low.
set -euo pipefail
cd "$(dirname "$0")/../.."

VHS_IMAGE="ghcr.io/charmbracelet/vhs:v0.12.1@sha256:ea49a6a1c529be83153e88321892b5585964418f0b9055e8c1e0d732194234a3"
for f in docs/demo/*.mp4; do
  name="$(basename "$f" .mp4)"
  echo "Writing docs/demo/$name.gif"
  docker run --rm -u "$(id -u):$(id -g)" -v "$PWD/docs/demo:/demo" --entrypoint ffmpeg "$VHS_IMAGE" \
    -loglevel error -y -i "/demo/$name.mp4" \
    -vf "fps=12,scale=1100:-1:flags=lanczos,split[a][b];[a]palettegen=max_colors=64:stats_mode=diff[p];[b][p]paletteuse=dither=none:diff_mode=rectangle" \
    "/demo/$name.gif"
done
