# syntax=docker/dockerfile:1.7
#
# Two runtime targets:
#   docker build --target cpu  -t vozgo:cpu  .    (default, portable)
#   docker build --target cuda -t vozgo:cuda .    (needs nvidia-container-toolkit)
#
# vozgo itself is pure Go (CGO_ENABLED=0); only whisper.cpp is compiled per backend.

ARG GO_VERSION=1.25
ARG NODE_VERSION=22
ARG WHISPER_VERSION=v1.9.3
ARG CUDA_IMAGE=12.6.2
# 75 = Turing (GTX 16xx / RTX 20xx). 86 = Ampere, 89 = Ada, 61 = Pascal.
ARG CUDA_ARCH=75
# Trabajos de compilación paralelos. nvcc pide ~2 GB por trabajo, así que el
# stage CUDA usa pocos: con -j"$(nproc)" el daemon de Docker se queda sin
# memoria ("cannot allocate memory") en máquinas con poca RAM asignada.
ARG BUILD_JOBS=0
ARG CUDA_BUILD_JOBS=2

# -------------------------------------------------------------------- web UI ---
# La UI se recompila desde web/app en cada build de imagen: web/dist está en el
# repo solo para que `go build` funcione sin Node, y así no puede quedar vieja.
FROM node:${NODE_VERSION}-alpine AS web-build
WORKDIR /app
COPY web/app/package.json web/app/package-lock.json ./
RUN npm ci --no-audit --no-fund
COPY web/app ./
RUN npm run build

# ---------------------------------------------------------------- go binary ---
FROM golang:${GO_VERSION}-bookworm AS go-build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=web-build /dist ./web/dist
ARG VERSION=dev
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath \
        -ldflags "-s -w -X main.version=${VERSION}" \
        -o /out/vozgo ./cmd/vozgo

# ------------------------------------------------------- whisper.cpp (CUDA) ---
FROM nvidia/cuda:${CUDA_IMAGE}-devel-ubuntu24.04 AS whisper-cuda
ARG WHISPER_VERSION
ARG CUDA_ARCH
ARG CUDA_BUILD_JOBS
RUN apt-get update && apt-get install -y --no-install-recommends \
        git cmake build-essential ca-certificates \
    && rm -rf /var/lib/apt/lists/*
RUN git clone --depth 1 --branch ${WHISPER_VERSION} \
        https://github.com/ggml-org/whisper.cpp /whisper
WORKDIR /whisper
RUN set -eu; \
    cmake -B build \
        -DCMAKE_BUILD_TYPE=Release \
        -DBUILD_SHARED_LIBS=OFF \
        -DGGML_CUDA=1 \
        -DCMAKE_CUDA_ARCHITECTURES="${CUDA_ARCH}" \
        -DWHISPER_BUILD_TESTS=OFF \
        -DWHISPER_BUILD_SERVER=OFF; \
    cmake --build build -j"${CUDA_BUILD_JOBS}" --config Release --target whisper-cli; \
    cp build/bin/whisper-cli /usr/local/bin/whisper-cli

# --------------------------------------------------------- runtime  (CUDA) ----
FROM nvidia/cuda:${CUDA_IMAGE}-runtime-ubuntu24.04 AS cuda
RUN apt-get update && apt-get install -y --no-install-recommends \
        ffmpeg libgomp1 ca-certificates curl \
    && rm -rf /var/lib/apt/lists/*
COPY --from=whisper-cuda /usr/local/bin/whisper-cli /usr/local/bin/whisper-cli
COPY --from=go-build /out/vozgo /usr/local/bin/vozgo
COPY scripts/entrypoint.sh /usr/local/bin/entrypoint.sh
COPY prompts /prompts
RUN chmod +x /usr/local/bin/entrypoint.sh \
    && mkdir -p /models /data/in /data/out /data/tmp \
    && useradd -u 1000 -m -s /usr/sbin/nologin vozgo \
    && chown -R 1000:1000 /models /data
USER 1000:1000
# El prompt por defecto sesga hacia español colombiano y hacia el vocabulario de
# trabajo: baja el WER a casi la mitad en estas notas. Se desactiva con
# VOZGO_PROMPT_FILE="" o se reemplaza montando otro archivo sobre /prompts.
ENV VOZGO_MODEL=/models/ggml-base.bin \
    VOZGO_LANGUAGE=auto \
    VOZGO_PROMPT_FILE=/prompts/es-CO.txt \
    VOZGO_TEMP_DIR=/data/tmp \
    VOZGO_ADDR=:8080 \
    VOZGO_MODEL_AUTO_DOWNLOAD=1
WORKDIR /data
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/entrypoint.sh"]
CMD ["serve"]

# -------------------------------------------------------- whisper.cpp (CPU) ---
FROM debian:bookworm-slim AS whisper-cpu
ARG WHISPER_VERSION
ARG BUILD_JOBS
RUN apt-get update && apt-get install -y --no-install-recommends \
        git cmake build-essential ca-certificates \
    && rm -rf /var/lib/apt/lists/*
RUN git clone --depth 1 --branch ${WHISPER_VERSION} \
        https://github.com/ggml-org/whisper.cpp /whisper
WORKDIR /whisper
RUN set -eu; \
    jobs="${BUILD_JOBS}"; \
    if [ "$jobs" = "0" ]; then jobs="$(nproc)"; fi; \
    cmake -B build \
        -DCMAKE_BUILD_TYPE=Release \
        -DBUILD_SHARED_LIBS=OFF \
        -DWHISPER_BUILD_TESTS=OFF \
        -DWHISPER_BUILD_SERVER=OFF; \
    cmake --build build -j"$jobs" --config Release --target whisper-cli; \
    cp build/bin/whisper-cli /usr/local/bin/whisper-cli

# ----------------------------------------------------------- runtime  (CPU) ---
# Last stage on purpose: a plain `docker build .` produces the portable image.
FROM debian:bookworm-slim AS cpu
RUN apt-get update && apt-get install -y --no-install-recommends \
        ffmpeg libgomp1 ca-certificates curl \
    && rm -rf /var/lib/apt/lists/*
COPY --from=whisper-cpu /usr/local/bin/whisper-cli /usr/local/bin/whisper-cli
COPY --from=go-build /out/vozgo /usr/local/bin/vozgo
COPY scripts/entrypoint.sh /usr/local/bin/entrypoint.sh
COPY prompts /prompts
RUN chmod +x /usr/local/bin/entrypoint.sh \
    && mkdir -p /models /data/in /data/out /data/tmp \
    && useradd -u 1000 -m -s /usr/sbin/nologin vozgo \
    && chown -R 1000:1000 /models /data
USER 1000:1000
# El prompt por defecto sesga hacia español colombiano y hacia el vocabulario de
# trabajo: baja el WER a casi la mitad en estas notas. Se desactiva con
# VOZGO_PROMPT_FILE="" o se reemplaza montando otro archivo sobre /prompts.
ENV VOZGO_MODEL=/models/ggml-base.bin \
    VOZGO_LANGUAGE=auto \
    VOZGO_PROMPT_FILE=/prompts/es-CO.txt \
    VOZGO_TEMP_DIR=/data/tmp \
    VOZGO_ADDR=:8080 \
    VOZGO_MODEL_AUTO_DOWNLOAD=1
WORKDIR /data
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/entrypoint.sh"]
CMD ["serve"]
