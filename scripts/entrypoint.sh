#!/bin/sh
# Fetches the configured model on first boot (the only network access vozgo
# ever makes), then execs the real binary.
set -eu

MODEL="${VOZGO_MODEL:-/models/ggml-base.bin}"
AUTO="${VOZGO_MODEL_AUTO_DOWNLOAD:-1}"
BASE_URL="${VOZGO_MODEL_BASE_URL:-https://huggingface.co/ggerganov/whisper.cpp/resolve/main}"

if [ ! -f "$MODEL" ] && [ "$AUTO" = "1" ]; then
    name=$(basename "$MODEL")
    dir=$(dirname "$MODEL")
    mkdir -p "$dir"
    echo "vozgo: modelo $name no encontrado, descargando una sola vez..." >&2
    if ! curl -fL --retry 3 --progress-bar -o "$MODEL.part" "$BASE_URL/$name"; then
        rm -f "$MODEL.part"
        echo "vozgo: no se pudo descargar $name desde $BASE_URL" >&2
        echo "vozgo: baja el modelo a mano y montalo en $dir" >&2
        exit 1
    fi
    mv "$MODEL.part" "$MODEL"
    echo "vozgo: modelo listo en $MODEL" >&2
fi

exec /usr/local/bin/vozgo "$@"
