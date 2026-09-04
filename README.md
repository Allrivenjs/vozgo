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

> **Docker Desktop no sirve para esto.** El soporte de GPU de Docker Desktop existe
> solo en Windows con WSL2 ([docs](https://docs.docker.com/desktop/features/gpu/));
> en Linux no expone la tarjeta. El target `cuda` necesita el **daemon nativo**:
>
> ```bash
> sudo systemctl enable --now docker
> sudo pacman -S nvidia-container-toolkit          # Arch
> sudo nvidia-ctk runtime configure --runtime=docker
> sudo systemctl restart docker
> docker context use default                       # volver: docker context use desktop-linux
> ```

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
vozgo wer <directorio>
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
| `-prompt-file` | — | Archivo con el prompt (`prompts/es-CO.txt`) |
| `-translate` | `false` | Traduce a inglés en vez de transcribir |
| `-merge` | — | Junta todo en un solo archivo, en orden |
| `-merge-plain` | `false` | En el archivo unido, sin encabezado por audio |
| `-skip-duplicates` | `true` | No transcribe dos veces archivos idénticos |
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

# una tanda de notas encadenadas -> un solo texto, en orden cronológico
vozgo transcribe -lang es -merge conversacion.md ./notas-del-dia
```

Con `-merge`, las notas se escriben en el orden en que se pasaron (para las notas
de WhatsApp, el nombre ya es la marca de tiempo, así que ordenar por nombre es
ordenar por hora). Los archivos idénticos —la misma nota descargada dos veces,
`audio.ogg` y `audio (1).ogg`— se detectan por hash y se transcriben una sola vez;
se conserva el del nombre original.

Sale con código ≠ 0 si algún archivo falló, así se puede usar en scripts.

## API HTTP

`vozgo serve` levanta en `:8080`:

| Método | Ruta | Qué hace |
|---|---|---|
| `GET` | `/` | UI web (drag & drop) |
| `GET` | `/api/health`, `/healthz` | Estado, modelo, workers |
| `POST` | `/api/transcribe` | `multipart/form-data`, uno o varios archivos → jobs |
| `GET` | `/api/jobs` | Lista de trabajos, más recientes primero |
| `GET` | `/api/events` | Stream SSE: un evento por cambio de estado |
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

La transcripción es asíncrona: `POST` responde `202` con los `job_id` y el progreso
llega por `/api/events` (server-sent events), que es lo que usa la UI. Si el stream
no está disponible, la UI cae a polling sola.

```bash
curl -N http://localhost:8080/api/events
# event: job
# data: {"id":"...","filename":"nota.ogg","status":"running",...}
```

## Modelos

`make model MODEL=<nombre>` o `./scripts/download-model.sh <nombre>`:

| Modelo | Tamaño | RAM necesaria | Notas |
|---|---|---|---|
| `tiny` | 75 MB | ~0,3 GB | Muy rápido, calidad pobre en español |
| `base` | 142 MB | ~0,5 GB | Decente para notas cortas |
| `small` | 466 MB | **1,3 GB medido** | El techo si el contenedor tiene ~2 GB |
| `medium` | 1,5 GB | **3,8 GB medido** | Peor relación que turbo: más lento y menos preciso |
| `large-v3-turbo` | 1,6 GB | **3,4 GB medido** | **El recomendado** si hay 4 GB disponibles |

**La RAM manda, y con margen**: whisper necesita cerca de **3× el tamaño del `.bin`**
(medido: `small`, 466 MB de archivo, llega a 1337 MiB de pico). Si el contenedor no
tiene esa memoria, el kernel mata el proceso a mitad de la carga; vozgo lo detecta y
lo dice, en vez de repetir la última línea de whisper, que habla del audio y manda a
depurar en la dirección equivocada.

Con `VOZGO_MODEL_NAME` eliges el archivo dentro de `./models` en Docker:

```bash
./scripts/download-model.sh small
VOZGO_MODEL_NAME=ggml-small.bin docker compose --profile cpu up
```

## Configuración por entorno

`VOZGO_MODEL`, `VOZGO_LANGUAGE`, `VOZGO_FORMATS`, `VOZGO_WORKERS`, `VOZGO_THREADS`,
`VOZGO_PROMPT`, `VOZGO_TEMP_DIR`, `VOZGO_ADDR`, `VOZGO_UPLOAD_DIR`,
`VOZGO_MAX_JOBS`, `VOZGO_JOB_TTL`,
`VOZGO_WHISPER_BIN`, `VOZGO_FFMPEG_BIN`, `VOZGO_FFPROBE_BIN`,
`VOZGO_MODEL_AUTO_DOWNLOAD` (`0` desactiva la descarga automática en Docker).

Copia `.env.example` a `.env` y compose lo lee solo. Los flags de CLI siempre ganan
sobre el entorno.

### Dialecto y vocabulario propio

Whisper no tiene código de idioma por país: `-lang es` es español neutro y por eso
tropieza con el habla colombiana. La palanca es el prompt inicial, que sesga el
decodificado hacia un dialecto y un vocabulario.

vozgo trae `prompts/es-CO.txt` (español colombiano más la jerga de trabajo) y lo usa
**por defecto en Docker**. En la medición de arriba baja el error de 9,6 % a 4,8 %, y
arregla exactamente lo que fallaba: "en observación de huelven" pasa a ser
"hacen observaciones, devuelven", y `pdf` a `PDF`.

```bash
vozgo transcribe -lang es -prompt-file prompts/es-CO.txt ./audios   # dialecto
vozgo transcribe -lang es -prompt "Adipa, Keycloak, ORL" ./audios   # ajuste puntual
VOZGO_PROMPT_FILE= docker compose --profile cpu up                  # desactivarlo
```

Escribe el tuyo copiando ese archivo: texto normal, no una lista de palabras sueltas,
y por debajo de ~900 caracteres, que es el límite que whisper aprovecha (más allá
descarta el principio en silencio; vozgo te avisa si te pasas).

### Historial del servidor

`vozgo serve` guarda los trabajos en memoria. Para que un servidor de larga vida no
crezca sin fin, olvida los terminados: `-max-jobs` (200 por defecto) y `-job-ttl`
(24 h). Los trabajos en cola o en curso nunca se descartan. Con `0` en ambos se
guarda todo, como antes.

## Estructura

```
cmd/vozgo/          CLI (transcribe, serve, wer)
internal/audio/     ffmpeg/ffprobe: decodificación a WAV 16 kHz mono
internal/whisper/   ejecución de whisper-cli y parseo de su JSON
internal/transcribe/ cola, pool de workers y estado de los jobs
internal/format/    render a txt/srt/vtt/json/md
internal/wer/       medición de calidad contra transcripciones humanas
internal/httpapi/   API JSON + UI embebida
web/app/            UI en React + TypeScript (fuente, Vite)
web/dist/           bundle compilado que el binario embebe
prompts/            prompts de dialecto (es-CO por defecto)
scripts/            descarga de modelos y entrypoint del contenedor
```

## Desarrollo

```bash
make check   # gofmt + go vet + go test
make build   # binario, usando el web/dist ya versionado
```

Los tests no necesitan ffmpeg ni whisper: usan binarios falsos para ejercitar el
pipeline completo, incluidas las rutas HTTP.

### La UI

Es React + TypeScript con Vite, en `web/app`, y el bundle compilado
(`web/dist`) va versionado para que `go build` funcione **sin Node instalado**.
La imagen de Docker recompila la UI desde el código en cada build, así que nunca
puede publicar un bundle viejo.

```bash
make web           # recompila web/dist (necesita Node)
make web-docker    # lo mismo sin Node, dentro de un contenedor
cd web/app && npm run dev   # UI con recarga en caliente contra `vozgo serve` en :8080
```

Tras tocar la UI hay que reconstruir `web/dist` y commitearlo: es lo que embebe
`go:embed`.

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

Todo con el prompt `es-CO`, en CPU (16 hilos), sobre 138 s de audio:

| Modelo | WER nota corta | WER nota larga | Tiempo | Pico de RAM |
|---|---|---|---|---|
| `base` | 18,1 % | 25,5 % | 8 s | ~0,5 GB |
| `small` | 4,8 % | 11,2 % | 16 s | 1,3 GB |
| `medium` | 3,6 % | 10,7 % | 66 s | 3,8 GB |
| **`large-v3-turbo`** | **1,2 %** | **5,5 %** | **27 s** | 3,4 GB |

`large-v3-turbo` gana en las dos cosas: **la mitad de error que `small` y más rápido
que `medium`** (tiene menos capas de decodificador, por eso adelanta a un modelo más
pequeño). Si tienes 4 GB para el contenedor, es la elección obvia.

El prompt de dialecto sigue siendo la mejora más barata: sin él, `small` daba 9,6 %
y 13,3 %; con él, 4,8 % y 11,2 %, sin gastar un byte más de RAM.

Mídelo tú mismo con el propio binario:

```bash
vozgo wer <dir>    # espera <dir>/ref/*.txt (transcripción humana)
                   # y <dir>/<variante>/*.txt (salidas de vozgo)
```

Velocidad en esta máquina (16 hilos, CPU, sin GPU): `base` transcribe 3 notas
(~3 min de audio) en 8,5 s con 3 workers × 4 hilos; `small` va a ~0,5× tiempo real
con 1 worker × 16 hilos.

**Ojo con Docker Desktop:** su VM trae una asignación de memoria fija y baja
(aquí 3 GB, visible en `docker info` → `MemTotal` y en
`~/.docker/desktop/settings-store.json` → `MemoryMiB`). Con eso, `medium` y `large`
no caben ni con un worker, y compilar el target `cuda` falla con
`cannot allocate memory`. Súbela en *Settings → Resources → Memory* si quieres
modelos grandes, o quédate en `small`, que es el punto dulce.

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
- No hay estado en disco: los trabajos viven en memoria y se pierden al reiniciar el
  contenedor (los archivos ya escritos, no).

## Licencia

MIT — ver [LICENSE](LICENSE).
