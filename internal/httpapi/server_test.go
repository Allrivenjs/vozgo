package httpapi

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Allrivenjs/vozgo/internal/config"
	"github.com/Allrivenjs/vozgo/internal/transcribe"
)

// newTestServer wires the API to a service backed by stub binaries.
func newTestServer(t *testing.T) *Server {
	t.Helper()
	dir := t.TempDir()

	write := func(name, body string) string {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
		return path
	}
	ffmpeg := write("ffmpeg", "#!/bin/sh\neval last=\\${$#}\nprintf 'RIFF' > \"$last\"\n")
	ffprobe := write("ffprobe", "#!/bin/sh\necho codec_name=opus\necho duration=1.0\n")
	whisper := write("whisper-cli", `#!/bin/sh
out=""
while [ $# -gt 0 ]; do
  case "$1" in
    -of) out="$2"; shift 2 ;;
    *) shift ;;
  esac
done
cat > "$out.json" <<JSON
{"result":{"language":"es"},
 "transcription":[{"offsets":{"from":0,"to":1000},"text":" Prueba HTTP."}]}
JSON
`)
	model := write("ggml-fake.bin", "model")

	cfg := config.Default()
	cfg.ModelPath = model
	cfg.FFmpegBin, cfg.FFprobeBin, cfg.WhisperBin = ffmpeg, ffprobe, whisper
	cfg.Workers, cfg.Threads = 1, 1
	cfg.Formats = []string{"txt"}

	svc, err := transcribe.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	svc.Start(context.Background())
	t.Cleanup(svc.Wait)

	return New(svc, Options{UploadDir: filepath.Join(dir, "uploads"), Version: "test"})
}

func uploadBody(t *testing.T, names ...string) (io.Reader, string) {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	for _, n := range names {
		part, err := mw.CreateFormFile("files", n)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := part.Write([]byte("fake ogg bytes")); err != nil {
			t.Fatal(err)
		}
	}
	if err := mw.Close(); err != nil {
		t.Fatal(err)
	}
	return &buf, mw.FormDataContentType()
}

// waitFor polls a job until it reaches a terminal state.
func waitFor(t *testing.T, srv *Server, id string) transcribe.Snapshot {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/jobs/"+id, nil))
		var snap transcribe.Snapshot
		if err := json.Unmarshal(rec.Body.Bytes(), &snap); err != nil {
			t.Fatalf("decoding job: %v (%s)", err, rec.Body.String())
		}
		if snap.Status == transcribe.StatusDone || snap.Status == transcribe.StatusFailed {
			return snap
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("job %s never finished", id)
	return transcribe.Snapshot{}
}

func TestUploadTranscribeDownload(t *testing.T) {
	srv := newTestServer(t)

	body, ctype := uploadBody(t, "nota.ogg", "otra.ogg")
	req := httptest.NewRequest(http.MethodPost, "/api/transcribe", body)
	req.Header.Set("Content-Type", ctype)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("upload status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var accepted struct {
		Jobs []struct {
			JobID    string `json:"job_id"`
			Filename string `json:"filename"`
		} `json:"jobs"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &accepted); err != nil {
		t.Fatal(err)
	}
	if len(accepted.Jobs) != 2 {
		t.Fatalf("accepted %d jobs, want 2", len(accepted.Jobs))
	}

	for _, j := range accepted.Jobs {
		snap := waitFor(t, srv, j.JobID)
		if snap.Status != transcribe.StatusDone {
			t.Fatalf("job %s failed: %s", j.Filename, snap.Err)
		}
		if snap.Text != "Prueba HTTP." {
			t.Errorf("text = %q", snap.Text)
		}
	}

	// Download as SRT even though the service default format is txt.
	id := accepted.Jobs[0].JobID
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/jobs/"+id+"/download?format=srt", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("download status = %d", rec.Code)
	}
	if !bytes.Contains(rec.Body.Bytes(), []byte("00:00:00,000 --> 00:00:01,000")) {
		t.Errorf("srt body = %q", rec.Body.String())
	}
	if cd := rec.Header().Get("Content-Disposition"); cd == "" {
		t.Error("expected a Content-Disposition header on downloads")
	}

	// Listing then deleting.
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/jobs", nil))
	var list struct{ Jobs []transcribe.Snapshot }
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	if len(list.Jobs) != 2 {
		t.Fatalf("listed %d jobs, want 2", len(list.Jobs))
	}

	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodDelete, "/api/jobs/"+id, nil))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("delete status = %d", rec.Code)
	}
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/jobs/"+id, nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("get after delete = %d, want 404", rec.Code)
	}
}

func TestUploadRejectsEmptyRequest(t *testing.T) {
	srv := newTestServer(t)
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	_ = mw.WriteField("note", "no files here")
	_ = mw.Close()

	req := httptest.NewRequest(http.MethodPost, "/api/transcribe", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestUploadRejectsNonMultipart(t *testing.T) {
	srv := newTestServer(t)
	req := httptest.NewRequest(http.MethodPost, "/api/transcribe", bytes.NewBufferString("{}"))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestDownloadUnknownFormat(t *testing.T) {
	srv := newTestServer(t)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/jobs/abc/download?format=pdf", nil))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestHealthAndIndex(t *testing.T) {
	srv := newTestServer(t)

	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/health", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("health status = %d", rec.Code)
	}
	var health map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &health); err != nil {
		t.Fatal(err)
	}
	if health["status"] != "ok" || health["model"] != "ggml-fake" {
		t.Errorf("health = %v", health)
	}

	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("index status = %d", rec.Code)
	}
	if !bytes.Contains(rec.Body.Bytes(), []byte("<title>vozgo")) {
		t.Error("index does not look like the embedded UI")
	}
}

func TestMaxUploadSize(t *testing.T) {
	srv := newTestServer(t)
	srv.opts.MaxUploadBytes = 10 // smaller than the multipart envelope

	body, ctype := uploadBody(t, "big.ogg")
	req := httptest.NewRequest(http.MethodPost, "/api/transcribe", body)
	req.Header.Set("Content-Type", ctype)
	req.Header.Set("Content-Length", strconv.Itoa(1<<20))
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code == http.StatusAccepted {
		t.Fatal("oversized upload should be rejected")
	}
}

func TestEventsStreamsJobUpdates(t *testing.T) {
	srv := newTestServer(t)
	ts := httptest.NewServer(srv)
	defer ts.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, ts.URL+"/api/events", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("Content-Type = %q, want text/event-stream", ct)
	}

	// The subscription must be registered before the upload, otherwise the
	// first transitions race the connection.
	deadline := time.Now().Add(2 * time.Second)
	for srv.svc.Subscribers() == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if srv.svc.Subscribers() == 0 {
		t.Fatal("el stream no se registró como suscriptor")
	}

	body, ctype := uploadBody(t, "evento.ogg")
	up, err := http.Post(ts.URL+"/api/transcribe", ctype, body)
	if err != nil {
		t.Fatal(err)
	}
	up.Body.Close()

	// Read events until the job reports done.
	scanner := bufio.NewScanner(resp.Body)
	sawQueued, sawDone := false, false
	for scanner.Scan() && !sawDone {
		line := scanner.Text()
		data, found := strings.CutPrefix(line, "data: ")
		if !found {
			continue
		}
		var snap transcribe.Snapshot
		if err := json.Unmarshal([]byte(data), &snap); err != nil {
			t.Fatalf("evento ilegible %q: %v", data, err)
		}
		if snap.Filename != "evento.ogg" {
			continue
		}
		switch snap.Status {
		case transcribe.StatusQueued, transcribe.StatusRunning:
			sawQueued = true
		case transcribe.StatusDone:
			sawDone = true
			if snap.Text != "Prueba HTTP." {
				t.Errorf("texto en el evento = %q", snap.Text)
			}
		case transcribe.StatusFailed:
			t.Fatalf("el trabajo falló: %s", snap.Err)
		}
	}
	if !sawQueued {
		t.Error("no llegó ningún evento previo al final")
	}
	if !sawDone {
		t.Fatal("no llegó el evento de trabajo terminado")
	}
}

func TestEventsUnsubscribesOnDisconnect(t *testing.T) {
	srv := newTestServer(t)
	ts := httptest.NewServer(srv)
	defer ts.Close()

	ctx, cancel := context.WithCancel(context.Background())
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, ts.URL+"/api/events", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}

	deadline := time.Now().Add(2 * time.Second)
	for srv.svc.Subscribers() == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if srv.svc.Subscribers() != 1 {
		t.Fatalf("suscriptores = %d, se esperaba 1", srv.svc.Subscribers())
	}

	cancel()
	resp.Body.Close()

	deadline = time.Now().Add(2 * time.Second)
	for srv.svc.Subscribers() > 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if got := srv.svc.Subscribers(); got != 0 {
		t.Errorf("suscriptores tras desconectar = %d, se esperaba 0", got)
	}
}

// TestEventsThroughLoggingMiddleware guards the wiring the unit tests miss:
// LogRequests wraps the ResponseWriter, and without an Unwrap method
// http.ResponseController cannot flush, so the stream dies on connect.
func TestEventsThroughLoggingMiddleware(t *testing.T) {
	srv := newTestServer(t)
	handler := LogRequests(slog.New(slog.NewTextHandler(io.Discard, nil)), srv)
	ts := httptest.NewServer(handler)
	defer ts.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, ts.URL+"/api/events", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	deadline := time.Now().Add(2 * time.Second)
	for srv.svc.Subscribers() == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}

	body, ctype := uploadBody(t, "mw.ogg")
	up, err := http.Post(ts.URL+"/api/transcribe", ctype, body)
	if err != nil {
		t.Fatal(err)
	}
	up.Body.Close()

	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() {
		data, found := strings.CutPrefix(scanner.Text(), "data: ")
		if !found {
			continue
		}
		var snap transcribe.Snapshot
		if err := json.Unmarshal([]byte(data), &snap); err != nil {
			t.Fatalf("evento ilegible: %v", err)
		}
		if snap.Status == transcribe.StatusDone {
			return // llegó el evento final a través del middleware
		}
	}
	t.Fatal("el stream se cerró sin entregar el evento final a través del middleware")
}
