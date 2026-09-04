// Package httpapi exposes the transcription service over HTTP: a JSON API plus
// the embedded single-page UI for drag-and-drop uploads.
package httpapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Allrivenjs/vozgo/internal/format"
	"github.com/Allrivenjs/vozgo/internal/transcribe"
	"github.com/Allrivenjs/vozgo/web"
)

// Options configures the HTTP layer.
type Options struct {
	// MaxUploadBytes caps a single request body. 0 falls back to 256 MiB.
	MaxUploadBytes int64
	// UploadDir is where uploads are staged before transcription.
	UploadDir string
	// Version is reported by /api/health.
	Version string
	Logger  *slog.Logger
}

// Server wires the service to a http.Handler.
type Server struct {
	svc  *transcribe.Service
	opts Options
	log  *slog.Logger
	mux  *http.ServeMux
}

// New builds the router.
func New(svc *transcribe.Service, opts Options) *Server {
	if opts.MaxUploadBytes <= 0 {
		opts.MaxUploadBytes = 256 << 20
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	s := &Server{svc: svc, opts: opts, log: opts.Logger, mux: http.NewServeMux()}
	s.routes()
	return s
}

// ServeHTTP implements http.Handler.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mux.ServeHTTP(w, r)
}

func (s *Server) routes() {
	s.mux.HandleFunc("GET /{$}", s.handleIndex)
	if assets, err := web.FS(); err == nil {
		// Los bundles llevan hash en el nombre: se pueden cachear para siempre.
		s.mux.Handle("GET /assets/", cacheForever(http.FileServerFS(assets)))
		// El favicon vive en la raíz del bundle y el navegador lo pide solo.
		s.mux.Handle("GET /favicon.svg", http.FileServerFS(assets))
		s.mux.HandleFunc("GET /favicon.ico", func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, "/favicon.svg", http.StatusMovedPermanently)
		})
	}
	s.mux.HandleFunc("GET /api/health", s.handleHealth)
	s.mux.HandleFunc("GET /healthz", s.handleHealth)
	s.mux.HandleFunc("POST /api/transcribe", s.handleUpload)
	s.mux.HandleFunc("GET /api/jobs", s.handleJobs)
	s.mux.HandleFunc("GET /api/events", s.handleEvents)
	s.mux.HandleFunc("GET /api/jobs/{id}", s.handleJob)
	s.mux.HandleFunc("DELETE /api/jobs/{id}", s.handleDeleteJob)
	s.mux.HandleFunc("GET /api/jobs/{id}/download", s.handleDownload)
}

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	page, err := web.Index()
	if err != nil {
		http.Error(w, "ui unavailable", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(page)
}

// cacheForever marca los assets con hash como inmutables: cambian de nombre en
// cada build, así que el navegador nunca sirve uno viejo por error.
func cacheForever(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		h.ServeHTTP(w, r)
	})
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	cfg := s.svc.Config()
	stats := s.svc.Stats()
	writeJSON(w, http.StatusOK, map[string]any{
		"status":   "ok",
		"version":  s.opts.Version,
		"model":    cfg.ModelName(),
		"language": cfg.Language,
		"workers":  cfg.Workers,
		"threads":  cfg.Threads,
		"formats":  s.svc.Formats(),
		"jobs":     stats,
	})
}

// handleUpload accepts one or more files under any multipart field name and
// enqueues a job per file. Files are streamed to disk, never buffered whole.
func (s *Server) handleUpload(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, s.opts.MaxUploadBytes)
	reader, err := r.MultipartReader()
	if err != nil {
		writeError(w, http.StatusBadRequest, "expected a multipart/form-data body")
		return
	}

	type accepted struct {
		JobID    string `json:"job_id"`
		Filename string `json:"filename"`
	}
	var jobs []accepted
	var rejected []string

	for {
		part, err := reader.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			writeError(w, http.StatusBadRequest, "malformed upload: "+err.Error())
			return
		}
		name := part.FileName()
		if name == "" {
			_ = part.Close()
			continue
		}
		path, err := s.stage(part, name)
		_ = part.Close()
		if err != nil {
			s.log.Error("staging upload failed", "file", name, "err", err)
			rejected = append(rejected, name)
			continue
		}
		id, err := s.svc.Submit(transcribe.Request{
			SourcePath:   path,
			Filename:     name,
			DeleteSource: true,
		})
		if err != nil {
			_ = os.Remove(path)
			rejected = append(rejected, name)
			continue
		}
		jobs = append(jobs, accepted{JobID: id, Filename: name})
	}

	if len(jobs) == 0 {
		writeError(w, http.StatusBadRequest, "no files in request")
		return
	}
	payload := map[string]any{"jobs": jobs}
	if len(rejected) > 0 {
		payload["rejected"] = rejected
	}
	writeJSON(w, http.StatusAccepted, payload)
}

// stage copies an uploaded part into UploadDir, keeping the original extension
// so ffmpeg can sniff the container.
func (s *Server) stage(part *multipart.Part, name string) (string, error) {
	dir := s.opts.UploadDir
	if dir == "" {
		dir = os.TempDir()
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	ext := strings.ToLower(filepath.Ext(name))
	f, err := os.CreateTemp(dir, "upload-*"+ext)
	if err != nil {
		return "", err
	}
	defer f.Close()
	if _, err := io.Copy(f, part); err != nil {
		_ = os.Remove(f.Name())
		return "", err
	}
	return f.Name(), nil
}

func (s *Server) handleJobs(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"jobs": s.svc.List()})
}

// handleEvents streams job transitions as server-sent events so the UI does not
// have to poll. Each event carries the job's full snapshot.
func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Connection", "keep-alive")
	// Proxies that buffer would defeat the point of streaming.
	w.Header().Set("X-Accel-Buffering", "no")

	rc := http.NewResponseController(w)
	events, cancel := s.svc.Subscribe(64)
	defer cancel()

	// Send the current state first so a page that connects mid-run is correct
	// without an extra fetch.
	for _, snap := range s.svc.List() {
		if err := writeEvent(w, snap); err != nil {
			return
		}
	}
	if err := rc.Flush(); err != nil {
		return
	}

	// A comment line keeps idle connections alive through proxies and lets the
	// server notice a client that went away.
	ping := time.NewTicker(20 * time.Second)
	defer ping.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case snap, ok := <-events:
			if !ok {
				return
			}
			if err := writeEvent(w, snap); err != nil {
				return
			}
			if err := rc.Flush(); err != nil {
				return
			}
		case <-ping.C:
			if _, err := io.WriteString(w, ": ping\n\n"); err != nil {
				return
			}
			if err := rc.Flush(); err != nil {
				return
			}
		}
	}
}

func writeEvent(w io.Writer, snap transcribe.Snapshot) error {
	payload, err := json.Marshal(snap)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(w, "event: job\ndata: %s\n\n", payload)
	return err
}

func (s *Server) handleJob(w http.ResponseWriter, r *http.Request) {
	snap, ok := s.svc.Get(r.PathValue("id"))
	if !ok {
		writeError(w, http.StatusNotFound, "job not found")
		return
	}
	writeJSON(w, http.StatusOK, snap)
}

func (s *Server) handleDeleteJob(w http.ResponseWriter, r *http.Request) {
	if !s.svc.Delete(r.PathValue("id")) {
		writeError(w, http.StatusNotFound, "job not found")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleDownload(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	f := r.URL.Query().Get("format")
	if f == "" {
		f = "txt"
	}
	if !format.Valid(f) {
		writeError(w, http.StatusBadRequest, "unknown format "+f)
		return
	}
	snap, ok := s.svc.Get(id)
	if !ok {
		writeError(w, http.StatusNotFound, "job not found")
		return
	}
	data, err := s.svc.Render(id, f)
	if err != nil {
		writeError(w, http.StatusConflict, err.Error())
		return
	}
	base := strings.TrimSuffix(filepath.Base(snap.Filename), filepath.Ext(snap.Filename))
	if base == "" {
		base = id
	}
	w.Header().Set("Content-Type", format.ContentType(f))
	w.Header().Set("Content-Disposition",
		fmt.Sprintf("attachment; filename=%q", base+format.Ext(f)))
	_, _ = w.Write(data)
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(v)
}

func writeError(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}

// LogRequests wraps h with a one-line access log.
func LogRequests(log *slog.Logger, h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, code: http.StatusOK}
		h.ServeHTTP(rec, r)
		log.Info("http",
			"method", r.Method,
			"path", r.URL.Path,
			"status", rec.code,
			"dur", time.Since(start).Round(time.Millisecond).String(),
		)
	})
}

type statusRecorder struct {
	http.ResponseWriter
	code int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.code = code
	r.ResponseWriter.WriteHeader(code)
}

// Unwrap exposes the underlying writer to http.ResponseController. Without it,
// Flush fails through this wrapper and the SSE stream closes immediately.
func (r *statusRecorder) Unwrap() http.ResponseWriter {
	return r.ResponseWriter
}
