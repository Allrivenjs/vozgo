#!/usr/bin/env bash
# Downloads a ggml whisper model into ./models for local (non-Docker) runs.
#
#   ./scripts/download-model.sh            # base
#   ./scripts/download-model.sh small
#   ./scripts/download-model.sh large-v3-turbo
#
# Sizes (approx): tiny 75M · base 142M · small 466M · medium 1.5G
#                 large-v3 3.1G · large-v3-turbo 1.6G
set -euo pipefail

MODEL="${1:-base}"
DEST_DIR="${2:-models}"
BASE_URL="${VOZGO_MODEL_BASE_URL:-https://huggingface.co/ggerganov/whisper.cpp/resolve/main}"
FILE="ggml-${MODEL}.bin"
DEST="${DEST_DIR}/${FILE}"

mkdir -p "$DEST_DIR"
if [[ -f "$DEST" ]]; then
    echo "ya existe: $DEST"
    exit 0
fi
echo "descargando $FILE -> $DEST"
curl -fL --retry 3 --progress-bar -o "${DEST}.part" "${BASE_URL}/${FILE}"
mv "${DEST}.part" "$DEST"
echo "listo: $DEST"
