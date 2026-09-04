// Command vozgo transcribes audio files (WhatsApp voice notes and friends) to
// text, fully offline, via ffmpeg + whisper.cpp.
package main

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/Allrivenjs/vozgo/internal/audio"
	"github.com/Allrivenjs/vozgo/internal/config"
	"github.com/Allrivenjs/vozgo/internal/httpapi"
	"github.com/Allrivenjs/vozgo/internal/transcribe"
	"github.com/Allrivenjs/vozgo/internal/wer"
)

// version is overridden at build time with -ldflags "-X main.version=...".
var version = "dev"

func main() {
	if err := run(os.Args[1:]); err != nil {
		if errors.Is(err, context.Canceled) {
			fmt.Fprintln(os.Stderr, "vozgo: cancelado")
			os.Exit(130)
		}
		fmt.Fprintln(os.Stderr, "vozgo:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		usage()
		return errors.New("falta un comando")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	switch args[0] {
	case "transcribe", "run", "t":
		return cmdTranscribe(ctx, args[1:])
	case "serve", "server", "s":
		return cmdServe(ctx, args[1:])
	case "wer":
		return cmdWER(args[1:])
	case "version", "-v", "--version":
		fmt.Println("vozgo", version)
		return nil
	case "help", "-h", "--help":
		usage()
		return nil
	default:
		usage()
		return fmt.Errorf("comando desconocido %q", args[0])
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `vozgo — notas de voz a texto, 100% local

Uso:
  vozgo transcribe [flags] <archivo|directorio>...   transcribe en lote
  vozgo serve [flags]                                API HTTP + UI web
  vozgo wer <directorio>                             mide la calidad vs. referencias
  vozgo version

Ejemplos:
  vozgo transcribe -out ./texto ~/Downloads/*.ogg
  vozgo transcribe -lang es -formats txt,srt -workers 4 ./audios
  vozgo serve -addr :8080

Variables de entorno: VOZGO_MODEL, VOZGO_LANGUAGE, VOZGO_THREADS, VOZGO_WORKERS,
VOZGO_FORMATS, VOZGO_WHISPER_BIN, VOZGO_FFMPEG_BIN, VOZGO_FFPROBE_BIN,
VOZGO_TEMP_DIR, VOZGO_PROMPT.

Flags por comando: vozgo transcribe -h | vozgo serve -h
`)
}

// bindCommon registers the flags shared by transcribe and serve.
func bindCommon(fs *flag.FlagSet, cfg *config.Config, formats *string) {
	fs.StringVar(&cfg.ModelPath, "model", cfg.ModelPath, "ruta al modelo ggml de whisper")
	fs.StringVar(&cfg.Language, "lang", cfg.Language, "idioma ISO (es, en...) o auto")
	fs.BoolVar(&cfg.Translate, "translate", cfg.Translate, "traducir a inglés en vez de transcribir")
	fs.IntVar(&cfg.Threads, "threads", cfg.Threads, "hilos por transcripción (0 = auto: nº CPU / workers)")
	fs.IntVar(&cfg.Workers, "workers", cfg.Workers, "archivos en paralelo (0 = auto)")
	fs.IntVar(&cfg.BeamSize, "beam", cfg.BeamSize, "beam search (0 = greedy)")
	fs.StringVar(&cfg.Prompt, "prompt", cfg.Prompt, "prompt inicial para sesgar el decodificado")
	fs.StringVar(&cfg.PromptFile, "prompt-file", cfg.PromptFile, "archivo con el prompt (p. ej. prompts/es-CO.txt); -prompt gana sobre él")
	fs.StringVar(&cfg.WhisperBin, "whisper-bin", cfg.WhisperBin, "binario whisper-cli")
	fs.StringVar(&cfg.FFmpegBin, "ffmpeg-bin", cfg.FFmpegBin, "binario ffmpeg")
	fs.StringVar(&cfg.FFprobeBin, "ffprobe-bin", cfg.FFprobeBin, "binario ffprobe")
	fs.StringVar(&cfg.TempDir, "temp-dir", cfg.TempDir, "directorio temporal (vacío = del sistema)")
	fs.StringVar(formats, "formats", strings.Join(cfg.Formats, ","), "formatos de salida: txt,srt,vtt,json,md")
}

func cmdTranscribe(ctx context.Context, args []string) error {
	cfg := config.Default()
	cfg.ApplyEnv()

	fset := flag.NewFlagSet("transcribe", flag.ContinueOnError)
	var formats, outDir, mergeTo string
	var recursive, quiet, stdout, mergePlain, skipDuplicates bool
	bindCommon(fset, &cfg, &formats)
	fset.StringVar(&outDir, "out", "", "directorio de salida (vacío = junto al audio)")
	fset.StringVar(&mergeTo, "merge", "", "junta todas las transcripciones en un solo archivo, en orden")
	fset.BoolVar(&mergePlain, "merge-plain", false, "en el archivo unido, sin encabezados por audio")
	fset.BoolVar(&skipDuplicates, "skip-duplicates", true, "no transcribir dos veces archivos idénticos")
	fset.BoolVar(&recursive, "recursive", true, "recorrer subdirectorios")
	fset.BoolVar(&stdout, "stdout", false, "imprimir el texto en stdout en vez de escribir archivos")
	fset.BoolVar(&quiet, "quiet", false, "sin progreso, solo errores")
	fset.BoolVar(&cfg.KeepWAV, "keep-wav", false, "conservar los WAV intermedios (debug)")
	if err := fset.Parse(args); err != nil {
		return err
	}
	cfg.Formats = config.SplitFormats(formats)
	if err := cfg.LoadPrompt(); err != nil {
		return err
	}

	inputs, err := collect(fset.Args(), recursive)
	if err != nil {
		return err
	}
	if len(inputs) == 0 {
		return errors.New("no se encontraron audios; pasa archivos o un directorio")
	}
	if skipDuplicates {
		var dropped []string
		inputs, dropped = dedupe(inputs)
		for _, d := range dropped {
			fmt.Fprintf(os.Stderr, "vozgo: %s es idéntico a otro archivo, se omite\n", filepath.Base(d))
		}
	}

	svc, err := transcribe.New(cfg)
	if err != nil {
		return err
	}
	cfg = svc.Config()
	warnOversubscribed(cfg)

	var mu sync.Mutex
	seen := make(map[string]transcribe.Status, len(inputs))
	total := len(inputs)
	if !quiet {
		svc.OnEvent = func(s transcribe.Snapshot) {
			mu.Lock()
			defer mu.Unlock()
			if seen[s.ID] == s.Status {
				return
			}
			seen[s.ID] = s.Status
			switch s.Status {
			case transcribe.StatusRunning:
				fmt.Fprintf(os.Stderr, "→ %s\n", s.Filename)
			case transcribe.StatusDone:
				fmt.Fprintf(os.Stderr, "✓ %s (%s, %s)\n", s.Filename,
					s.Language, time.Duration(s.ElapsedMS)*time.Millisecond)
			case transcribe.StatusFailed:
				fmt.Fprintf(os.Stderr, "✗ %s: %s\n", s.Filename, s.Err)
			}
		}
	}

	fmt.Fprintf(os.Stderr, "vozgo: %d archivo(s), modelo %s, %d worker(s), %d hilo(s)\n",
		total, cfg.ModelName(), cfg.Workers, cfg.Threads)

	svc.Start(ctx)
	ids := make([]string, 0, len(inputs))
	for _, in := range inputs {
		dest := outDir
		if dest == "" && !stdout {
			dest = filepath.Dir(in)
		}
		id, err := svc.Submit(transcribe.Request{
			SourcePath: in,
			Filename:   filepath.Base(in),
			OutputDir:  dest,
		})
		if err != nil {
			return err
		}
		ids = append(ids, id)
	}
	svc.Wait()

	if stdout {
		for _, id := range ids {
			snap, ok := svc.Get(id)
			if !ok || snap.Status != transcribe.StatusDone {
				continue
			}
			if total > 1 {
				fmt.Printf("=== %s ===\n", snap.Filename)
			}
			fmt.Println(snap.Text)
		}
	}

	if mergeTo != "" {
		written, err := mergeTranscripts(svc, ids, mergeTo, mergePlain)
		if err != nil {
			return err
		}
		fmt.Fprintf(os.Stderr, "unido: %d transcripciones en %s\n", written, mergeTo)
	}

	st := svc.Stats()
	fmt.Fprintf(os.Stderr, "listo: %d ok, %d con error de %d\n", st.Done, st.Failed, st.Total)
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if st.Failed > 0 {
		return fmt.Errorf("%d archivo(s) fallaron", st.Failed)
	}
	return nil
}

func cmdServe(ctx context.Context, args []string) error {
	cfg := config.Default()
	cfg.ApplyEnv()

	fset := flag.NewFlagSet("serve", flag.ContinueOnError)
	var formats, addr, uploadDir string
	var maxUploadMB int64
	bindCommon(fset, &cfg, &formats)
	fset.StringVar(&addr, "addr", envOr("VOZGO_ADDR", ":8080"), "dirección de escucha")
	fset.StringVar(&uploadDir, "upload-dir", envOr("VOZGO_UPLOAD_DIR", ""), "directorio para subidas (vacío = temporal)")
	fset.Int64Var(&maxUploadMB, "max-upload-mb", 256, "tamaño máximo por request en MiB")
	fset.IntVar(&cfg.MaxJobs, "max-jobs", cfg.MaxJobs, "trabajos terminados que se recuerdan (0 = sin límite)")
	fset.DurationVar(&cfg.JobTTL, "job-ttl", cfg.JobTTL, "cuánto se recuerda un trabajo terminado (0 = para siempre)")
	if err := fset.Parse(args); err != nil {
		return err
	}
	cfg.Formats = config.SplitFormats(formats)
	if err := cfg.LoadPrompt(); err != nil {
		return err
	}

	svc, err := transcribe.New(cfg)
	if err != nil {
		return err
	}
	cfg = svc.Config()
	warnOversubscribed(cfg)
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	svc.Start(ctx)

	srv := &http.Server{
		Addr: addr,
		Handler: httpapi.LogRequests(log, httpapi.New(svc, httpapi.Options{
			MaxUploadBytes: maxUploadMB << 20,
			UploadDir:      uploadDir,
			Version:        version,
			Logger:         log,
		})),
		ReadHeaderTimeout: 15 * time.Second,
		// No WriteTimeout: a long transcription download must not be cut off.
	}

	errCh := make(chan error, 1)
	go func() {
		log.Info("vozgo listening", "addr", addr, "model", cfg.ModelName(),
			"workers", cfg.Workers, "threads", cfg.Threads, "version", version)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		log.Info("shutting down")
		shutCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return srv.Shutdown(shutCtx)
	}
}

// cmdWER compara transcripciones contra referencias humanas. El directorio debe
// tener ref/<nombre>.txt con la transcripción correcta y un subdirectorio por
// variante a comparar, por ejemplo small/<nombre>.txt.
func cmdWER(args []string) error {
	fset := flag.NewFlagSet("wer", flag.ContinueOnError)
	if err := fset.Parse(args); err != nil {
		return err
	}
	root := "."
	if fset.NArg() > 0 {
		root = fset.Arg(0)
	}

	refDir := filepath.Join(root, "ref")
	refs, err := filepath.Glob(filepath.Join(refDir, "*.txt"))
	if err != nil {
		return err
	}
	if len(refs) == 0 {
		return fmt.Errorf("no hay referencias en %s (se esperan archivos .txt)", refDir)
	}
	sort.Strings(refs)

	entries, err := os.ReadDir(root)
	if err != nil {
		return err
	}
	var variants []string
	for _, e := range entries {
		if e.IsDir() && e.Name() != "ref" && e.Name() != "in" {
			variants = append(variants, e.Name())
		}
	}
	if len(variants) == 0 {
		return fmt.Errorf("no hay variantes que comparar en %s", root)
	}
	sort.Strings(variants)

	fmt.Printf("%-16s %-12s %9s %8s %9s\n", "archivo", "variante", "palabras", "WER", "acierto")
	for _, refPath := range refs {
		refText, err := os.ReadFile(refPath)
		if err != nil {
			return err
		}
		name := filepath.Base(refPath)
		for _, variant := range variants {
			hypPath := filepath.Join(root, variant, name)
			hypText, err := os.ReadFile(hypPath)
			if err != nil {
				continue // esa variante no transcribió este archivo
			}
			rate, words := wer.Compare(string(refText), string(hypText))
			fmt.Printf("%-16s %-12s %9d %7.1f%% %8.1f%%\n",
				strings.TrimSuffix(name, ".txt"), variant, words, rate*100, (1-rate)*100)
		}
	}
	return nil
}

// mergeTranscripts escribe todas las transcripciones en un solo archivo,
// respetando el orden en que se pasaron los audios. Un trabajo fallido no
// interrumpe el archivo: se anota y se sigue, para no perder lo que sí salió.
func mergeTranscripts(svc *transcribe.Service, ids []string, path string, plain bool) (int, error) {
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return 0, fmt.Errorf("creando %s: %w", dir, err)
		}
	}
	f, err := os.Create(path)
	if err != nil {
		return 0, fmt.Errorf("creando %s: %w", path, err)
	}
	defer f.Close()

	w := bufio.NewWriter(f)
	written := 0
	for _, id := range ids {
		snap, ok := svc.Get(id)
		if !ok {
			continue
		}
		if snap.Status != transcribe.StatusDone {
			if !plain {
				fmt.Fprintf(w, "## %s\n\n[sin transcripción: %s]\n\n", snap.Filename, snap.Err)
			}
			continue
		}
		if !plain {
			header := snap.Filename
			if d := time.Duration(snap.DurationMS) * time.Millisecond; d > 0 {
				header = fmt.Sprintf("%s (%s)", header, d.Round(time.Second))
			}
			fmt.Fprintf(w, "## %s\n\n", header)
		}
		fmt.Fprintf(w, "%s\n\n", snap.Text)
		written++
	}
	if err := w.Flush(); err != nil {
		return 0, err
	}
	return written, nil
}

// dedupe descarta archivos con contenido idéntico, que es lo que pasa cuando se
// descarga dos veces la misma nota ("audio.ogg" y "audio (1).ogg"). De cada
// grupo conserva el del nombre más corto, que es el original y no la copia, sin
// alterar el orden de la lista.
func dedupe(paths []string) (kept, dropped []string) {
	at := make(map[string]int, len(paths)) // hash -> posición en kept
	for _, p := range paths {
		sum, err := hashFile(p)
		if err != nil {
			kept = append(kept, p) // si no se puede leer, que falle al transcribir
			continue
		}
		i, dup := at[sum]
		if !dup {
			at[sum] = len(kept)
			kept = append(kept, p)
			continue
		}
		if len(filepath.Base(p)) < len(filepath.Base(kept[i])) {
			dropped = append(dropped, kept[i])
			kept[i] = p
			continue
		}
		dropped = append(dropped, p)
	}
	return kept, dropped
}

func hashFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// collect expands the CLI arguments into a sorted, de-duplicated file list.
// Explicit file arguments are taken as-is; directories are walked and filtered
// by extension.
func collect(args []string, recursive bool) ([]string, error) {
	seen := make(map[string]bool)
	var out []string
	add := func(p string) {
		abs, err := filepath.Abs(p)
		if err != nil {
			abs = p
		}
		if !seen[abs] {
			seen[abs] = true
			out = append(out, abs)
		}
	}

	for _, arg := range args {
		st, err := os.Stat(arg)
		if err != nil {
			return nil, fmt.Errorf("no se puede leer %q: %w", arg, err)
		}
		if !st.IsDir() {
			add(arg)
			continue
		}
		root := arg
		err = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				if !recursive && path != root {
					return fs.SkipDir
				}
				return nil
			}
			if audio.IsSupported(filepath.Ext(d.Name())) {
				add(path)
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	sort.Strings(out)
	return out, nil
}

// warnOversubscribed flags a thread budget above the CPU count: ggml spins
// while waiting, so oversubscription is far worse than a lower setting.
func warnOversubscribed(cfg config.Config) {
	if total, ncpu, yes := cfg.Oversubscribed(); yes {
		fmt.Fprintf(os.Stderr,
			"vozgo: aviso: %d workers × %d hilos = %d > %d CPUs; whisper se vuelve mucho más lento. Baja -workers o -threads.\n",
			cfg.Workers, cfg.Threads, total, ncpu)
	}
	if chars, limit, yes := cfg.PromptTooLong(); yes {
		fmt.Fprintf(os.Stderr,
			"vozgo: aviso: el prompt tiene %d caracteres y whisper solo usa ~%d; recorta el archivo o whisper descartará el principio.\n",
			chars, limit)
	}
	if need, budget, yes := cfg.MemoryTight(); yes {
		fmt.Fprintf(os.Stderr,
			"vozgo: aviso: %d workers con este modelo necesitan ~%d MiB y solo hay ~%d MiB; riesgo de OOM. Baja -workers o usa un modelo menor.\n",
			cfg.Workers, need>>20, budget>>20)
	}
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
