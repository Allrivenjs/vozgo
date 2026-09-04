# vozgo

Notas de voz a texto, **100% local**. Pensado para las notas de WhatsApp (`.ogg` / Opus),
pero traga cualquier cosa que ffmpeg pueda decodificar. CLI para lotes, API HTTP y una
UI web mínima de arrastrar y soltar. Nada sale a internet salvo la descarga del modelo,
una sola vez.

```
audio (.ogg/.opus/.m4a/.mp3/...)  →  ffmpeg (16 kHz mono PCM)  →  whisper.cpp  →  txt/srt/vtt/json/md
```

- **Go puro** (`CGO_ENABLED=0`); whisper.cpp se compila aparte, con backend CPU o CUDA.
- **Lotes en paralelo**: pool de workers, un proceso whisper por archivo.
- **Docker con dos targets**: `cpu` (portable) y `cuda` (NVIDIA).
- **Salidas**: `txt`, `srt`, `vtt`, `json` (con timestamps por segmento) y `md`.

## Arranque rápido (Docker, CPU)

```bash
git clone git@github.com:Allrivenjs/vozgo.git && cd vozgo
docker compose --profile cpu up --build
# abre http://localhost:8080 y arrastra tus audios
```

El modelo (`ggml-base.bin`, ~142 MB) se descarga automáticamente en el primer
arranque y queda cacheado en `./models`.

### Lote sin servidor

```bash
cp ~/Downloads/*.ogg ./audios/
docker compose --profile cli run --rm cli
# resultados en ./out
```

### Con GPU NVIDIA

Requiere [nvidia-container-toolkit](https://docs.nvidia.com/datacenter/cloud-native/container-toolkit/latest/install-guide.html).
`CUDA_ARCH` es la compute capability de tu tarjeta (75 = GTX 16xx / RTX 20xx,
86 = RTX 30xx, 89 = RTX 40xx, 61 = GTX 10xx):

```bash
CUDA_ARCH=75 docker compose --profile cuda up --build
```

## Uso local (sin Docker)

Necesitas `ffmpeg`, `ffprobe` y `whisper-cli` (de whisper.cpp) en el `PATH`.

```bash
make model MODEL=base      # descarga models/ggml-base.bin
make build                 # ./bin/vozgo

./bin/vozgo transcribe -model models/ggml-base.bin -lang es ~/Downloads/*.ogg
./bin/vozgo serve  -model models/ggml-base.bin
```

## CLI

```
vozgo transcribe [flags] <archivo|directorio>...
vozgo serve [flags]
vozgo version
```

Flags principales (`vozgo transcribe -h` para la lista completa):

| Flag | Default | Qué hace |
|---|---|---|
| `-model` | `models/ggml-base.bin` | Modelo ggml de whisper |
| `-lang` | `auto` | Idioma ISO (`es`, `en`…) o `auto` |
| `-formats` | `txt` | `txt,srt,vtt,json,md` |
| `-out` | junto al audio | Directorio de salida |
| `-workers` | auto | Archivos en paralelo (auto: CPU y RAM disponibles) |
| `-threads` | auto | Hilos por transcripción (auto: nº CPU / workers) |
| `-beam` | `0` | Beam search (0 = greedy, más rápido) |
| `-prompt` | — | Prompt inicial para sesgar vocabulario |
| `-translate` | `false` | Traduce a inglés en vez de transcribir |
| `-stdout` | `false` | Imprime el texto en vez de escribir archivos |
| `-recursive` | `true` | Recorre subdirectorios |

Ejemplos:

```bash
# lote a texto y subtítulos, 4 en paralelo
vozgo transcribe -lang es -formats txt,srt -workers 4 -out ./texto ./audios

# una nota a stdout, para encadenar con otra herramienta
vozgo transcribe -stdout -lang es "WhatsApp Ptt 2026-09-04 at 09.50.31.ogg" | less

# vocabulario propio (nombres, jerga) con prompt inicial
vozgo transcribe -lang es -prompt "Jaime, Adipa, Keycloak, Moodle" ./audios
```

Sale con código ≠ 0 si algún archivo falló, así se puede usar en scripts.

## API HTTP

`vozgo serve` levanta en `:8080`:

| Método | Ruta | Qué hace |
|---|---|---|
| `GET` | `/` | UI web (drag & drop) |
| `GET` | `/api/health`, `/healthz` | Estado, modelo, workers |
| `POST` | `/api/transcribe` | `multipart/form-data`, uno o varios archivos → jobs |
| `GET` | `/api/jobs` | Lista de trabajos, más recientes primero |
| `GET` | `/api/jobs/{id}` | Estado + texto del trabajo |
| `GET` | `/api/jobs/{id}/download?format=srt` | Descarga en el formato pedido |
| `DELETE` | `/api/jobs/{id}` | Olvida el trabajo |

```bash
# subir varias notas
curl -sS -F files=@nota1.ogg -F files=@nota2.ogg http://localhost:8080/api/transcribe

# consultar
curl -sS http://localhost:8080/api/jobs | jq '.jobs[] | {filename, status, text}'

# bajar subtítulos
curl -sSO -J "http://localhost:8080/api/jobs/<id>/download?format=srt"
```

La transcripción es asíncrona: `POST` responde `202` con los `job_id`, y el estado se
consulta por polling (la UI lo hace cada 1,5 s).

## Modelos

`make model MODEL=<nombre>` o `./scripts/download-model.sh <nombre>`:

| Modelo | Tamaño | Notas |
|---|---|---|
| `tiny` | 75 MB | Muy rápido, calidad pobre en español |
| `base` | 142 MB | Default, decente para notas cortas |
| `small` | 466 MB | Buen equilibrio en CPU |
| `medium` | 1,5 GB | Mejor calidad, pesado en CPU |
| `large-v3-turbo` | 1,6 GB | Lo mejor con GPU |

Con `VOZGO_MODEL_NAME` eliges el archivo dentro de `./models` en Docker:

```bash
./scripts/download-model.sh small
VOZGO_MODEL_NAME=ggml-small.bin docker compose --profile cpu up
```

## Configuración por entorno

`VOZGO_MODEL`, `VOZGO_LANGUAGE`, `VOZGO_FORMATS`, `VOZGO_WORKERS`, `VOZGO_THREADS`,
`VOZGO_PROMPT`, `VOZGO_TEMP_DIR`, `VOZGO_ADDR`, `VOZGO_UPLOAD_DIR`,
`VOZGO_WHISPER_BIN`, `VOZGO_FFMPEG_BIN`, `VOZGO_FFPROBE_BIN`,
`VOZGO_MODEL_AUTO_DOWNLOAD` (`0` desactiva la descarga automática en Docker).

Los flags de CLI siempre ganan sobre el entorno.

## Estructura

```
cmd/vozgo/          CLI (transcribe, serve)
internal/audio/     ffmpeg/ffprobe: decodificación a WAV 16 kHz mono
internal/whisper/   ejecución de whisper-cli y parseo de su JSON
internal/transcribe/ cola, pool de workers y estado de los jobs
internal/format/    render a txt/srt/vtt/json/md
internal/httpapi/   API JSON + UI embebida
web/                index.html (sin dependencias externas)
scripts/            descarga de modelos y entrypoint del contenedor
```

## Desarrollo

```bash
make check   # gofmt + go vet + go test
make build
```

Los tests no necesitan ffmpeg ni whisper: usan binarios falsos para ejercitar el
pipeline completo, incluidas las rutas HTTP.

## Rendimiento y memoria

`-workers` y `-threads` en `auto` se calculan solos, y conviene dejarlos así:

- **No sobresuscribas hilos.** ggml hace *busy-wait* entre hilos, así que pedir más
  hilos que CPUs lógicas no es "un poco más lento", es catastrófico: 2 workers × 16
  hilos en una máquina de 16 hilos se quedaron girando al 1788 % de CPU y no acabaron
  en 3 minutos un audio que con 4 hilos tarda 3,5 s. `auto` reparte el presupuesto:
  `threads = nº CPU / workers`.
- **La RAM manda.** Cada worker es un proceso whisper con su propia copia del modelo
  (≈ 2× el tamaño del `.bin`). `auto` mira el límite del cgroup y la memoria libre y
  baja los workers para no morir por OOM. Si fijas `-workers` a mano y no cabe, vozgo
  avisa por stderr.

### Calidad medida

Notas de voz reales de WhatsApp (español, audio de trabajo con jerga y siglas),
comparadas contra transcripción humana con `scripts/wer.py` (WER = word error rate,
menos es mejor; se normalizan mayúsculas, tildes y puntuación):

| Nota | `base` | `small` | `small` + `-prompt` |
|---|---|---|---|
| 26 s / 83 palabras | 18,1 % | 9,6 % | **7,2 %** |
| 112 s / 384 palabras | 25,5 % | **13,3 %** | 13,3 % |

`small` corta el error a la mitad frente a `base` y es el punto dulce en CPU. El
`-prompt` con el vocabulario del dominio (nombres propios, siglas) ayuda sobre todo
en audios cortos, donde whisper tiene poco contexto propio del que agarrarse.

```bash
python3 scripts/wer.py <dir>   # espera <dir>/ref/*.txt y <dir>/<modelo>/*.txt
```

Velocidad en esta máquina (16 hilos, CPU, sin GPU): `base` transcribe 3 notas
(~3 min de audio) en 8,5 s con 3 workers × 4 hilos; `small` va a ~0,5× tiempo real
con 1 worker × 16 hilos.

Si el daemon de Docker corre con poca RAM (aquí eran 3 GB: `docker info` →
`MemTotal`), `medium` y `large` no caben con varios workers. Amplía la memoria del
daemon o quédate en `small`.

## Notas

- whisper.cpp solo lee WAV PCM de 16 kHz; por eso ffmpeg es obligatorio.
- Los WAV intermedios viven en `VOZGO_TEMP_DIR` y se borran al terminar
  (`-keep-wav` los conserva para depurar).
- El target `cuda` compila whisper.cpp con `GGML_CUDA=1`, pero **no está probado en
  ejecución**: requiere `nvidia-container-toolkit` en el host (en Arch:
  `sudo pacman -S nvidia-container-toolkit`).
- **Compilar el target `cuda` necesita RAM**: nvcc usa ~2 GB por trabajo paralelo y
  con `-j$(nproc)` el build muere con `cannot allocate memory`. Por eso el stage CUDA
  usa `CUDA_BUILD_JOBS=2` por defecto; bájalo a `1` si tu daemon tiene poca memoria:

  ```bash
  make docker-cuda CUDA_BUILD_JOBS=1
  # o: CUDA_BUILD_JOBS=1 docker compose --profile cuda build
  ```
- La UI hace polling cada 1,5 s; no hay websockets ni estado en disco: los trabajos
  viven en memoria y se pierden al reiniciar el contenedor (las descargas ya hechas,
  no).

## Licencia

MIT — ver [LICENSE](LICENSE).
