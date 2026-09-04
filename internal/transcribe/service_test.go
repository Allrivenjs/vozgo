package transcribe

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/Allrivenjs/vozgo/internal/config"
)

// fakeBins writes stub ffmpeg/ffprobe/whisper-cli scripts so the pipeline can be
// exercised without the real (multi-hundred-MB) toolchain.
func fakeBins(t *testing.T, transcriptText string, whisperExit int) config.Config {
	t.Helper()
	dir := t.TempDir()

	write := func(name, body string) string {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
		return path
	}

	// ffmpeg: the destination WAV is the last argument.
	ffmpeg := write("ffmpeg", `#!/bin/sh
eval last=\${$#}
printf 'RIFFFAKEWAVE' > "$last"
`)
	ffprobe := write("ffprobe", `#!/bin/sh
echo "codec_name=opus"
echo "duration=2.500000"
`)
	// whisper-cli: find -of and write the JSON whisper.cpp would produce.
	whisper := write("whisper-cli", `#!/bin/sh
out=""
while [ $# -gt 0 ]; do
  case "$1" in
    -of) out="$2"; shift 2 ;;
    *) shift ;;
  esac
done
if [ "`+strconv.Itoa(whisperExit)+`" != "0" ]; then
  echo "fake whisper failure" >&2
  exit `+strconv.Itoa(whisperExit)+`
fi
cat > "$out.json" <<JSON
{"result":{"language":"es"},
 "transcription":[{"offsets":{"from":0,"to":2500},"text":" `+transcriptText+`"}]}
JSON
`)

	model := filepath.Join(dir, "ggml-fake.bin")
	if err := os.WriteFile(model, []byte("model"), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg := config.Default()
	cfg.ModelPath = model
	cfg.FFmpegBin = ffmpeg
	cfg.FFprobeBin = ffprobe
	cfg.WhisperBin = whisper
	cfg.Workers = 2
	cfg.Threads = 1
	cfg.Formats = []string{"txt", "srt"}
	return cfg
}

func TestServiceBatchWritesOutputs(t *testing.T) {
	cfg := fakeBins(t, "Hola mundo.", 0)
	svc, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}

	srcDir := t.TempDir()
	outDir := filepath.Join(srcDir, "out")
	var ids []string
	svc.Start(context.Background())
	for _, name := range []string{"nota1.ogg", "nota2.ogg"} {
		src := filepath.Join(srcDir, name)
		if err := os.WriteFile(src, []byte("fake ogg"), 0o644); err != nil {
			t.Fatal(err)
		}
		id, err := svc.Submit(Request{SourcePath: src, Filename: name, OutputDir: outDir})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	svc.Wait()

	if st := svc.Stats(); st.Done != 2 || st.Failed != 0 {
		t.Fatalf("stats = %+v, want 2 done 0 failed", st)
	}
	for _, id := range ids {
		snap, ok := svc.Get(id)
		if !ok {
			t.Fatalf("job %s missing", id)
		}
		if snap.Status != StatusDone {
			t.Fatalf("status = %s (%s)", snap.Status, snap.Err)
		}
		if snap.Text != "Hola mundo." {
			t.Errorf("text = %q", snap.Text)
		}
		if snap.Language != "es" {
			t.Errorf("language = %q, want es", snap.Language)
		}
		if snap.DurationMS != 2500 {
			t.Errorf("duration = %dms, want 2500 (from ffprobe)", snap.DurationMS)
		}
		if len(snap.Outputs) != 2 {
			t.Errorf("outputs = %v, want txt and srt", snap.Outputs)
		}
	}
	for _, name := range []string{"nota1.txt", "nota1.srt", "nota2.txt", "nota2.srt"} {
		if _, err := os.Stat(filepath.Join(outDir, name)); err != nil {
			t.Errorf("missing output %s: %v", name, err)
		}
	}
}

func TestServiceRecordsFailure(t *testing.T) {
	cfg := fakeBins(t, "", 3)
	svc, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	src := filepath.Join(t.TempDir(), "roto.ogg")
	if err := os.WriteFile(src, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	svc.Start(context.Background())
	id, err := svc.Submit(Request{SourcePath: src, Filename: "roto.ogg"})
	if err != nil {
		t.Fatal(err)
	}
	svc.Wait()

	snap, _ := svc.Get(id)
	if snap.Status != StatusFailed {
		t.Fatalf("status = %s, want failed", snap.Status)
	}
	if snap.Err == "" {
		t.Error("expected the whisper stderr to be reported")
	}
	// A failed job must not be renderable.
	if _, err := svc.Render(id, "txt"); err == nil {
		t.Error("Render on a failed job should error")
	}
}

func TestServiceDeleteSourceAndDeleteJob(t *testing.T) {
	cfg := fakeBins(t, "Borrar.", 0)
	svc, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	src := filepath.Join(t.TempDir(), "upload.ogg")
	if err := os.WriteFile(src, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	svc.Start(context.Background())
	id, err := svc.Submit(Request{SourcePath: src, Filename: "upload.ogg", DeleteSource: true})
	if err != nil {
		t.Fatal(err)
	}
	svc.Wait()

	if _, err := os.Stat(src); !os.IsNotExist(err) {
		t.Error("staged upload should be removed after the job finishes")
	}
	if got, err := svc.Render(id, "txt"); err != nil || string(got) != "Borrar.\n" {
		t.Errorf("Render = %q, %v", got, err)
	}
	if !svc.Delete(id) {
		t.Error("Delete should report success for a known id")
	}
	if _, ok := svc.Get(id); ok {
		t.Error("job should be gone after Delete")
	}
	if svc.Delete(id) {
		t.Error("Delete should report false for an unknown id")
	}
}

func TestServiceListNewestFirst(t *testing.T) {
	cfg := fakeBins(t, "ok", 0)
	cfg.Workers = 1
	svc, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	svc.Start(context.Background())
	for _, name := range []string{"a.ogg", "b.ogg"} {
		src := filepath.Join(dir, name)
		if err := os.WriteFile(src, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := svc.Submit(Request{SourcePath: src, Filename: name}); err != nil {
			t.Fatal(err)
		}
		time.Sleep(2 * time.Millisecond)
	}
	svc.Wait()

	list := svc.List()
	if len(list) != 2 || list[0].Filename != "b.ogg" {
		t.Fatalf("List order = %v, want b.ogg first", list)
	}
}

func TestNewRejectsBadFormat(t *testing.T) {
	cfg := fakeBins(t, "x", 0)
	cfg.Formats = []string{"docx"}
	if _, err := New(cfg); err == nil {
		t.Fatal("expected New to reject an unknown format")
	}
}

func TestServiceEvictsOldJobsByCount(t *testing.T) {
	cfg := fakeBins(t, "ok", 0)
	cfg.Workers = 1
	cfg.MaxJobs = 2
	cfg.JobTTL = 0
	svc, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}

	dir := t.TempDir()
	svc.Start(context.Background())
	var ids []string
	for i := range 5 {
		src := filepath.Join(dir, fmt.Sprintf("n%d.ogg", i))
		if err := os.WriteFile(src, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		id, err := svc.Submit(Request{SourcePath: src, Filename: filepath.Base(src)})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
		// Let the worker finish so the job is evictable on the next Submit.
		for range 100 {
			if snap, ok := svc.Get(id); ok && snap.Status == StatusDone {
				break
			}
			time.Sleep(5 * time.Millisecond)
		}
	}
	svc.Wait()

	if got := len(svc.List()); got > cfg.MaxJobs {
		t.Fatalf("quedaron %d trabajos, el tope es %d", got, cfg.MaxJobs)
	}
	// The newest submissions must be the survivors.
	if _, ok := svc.Get(ids[len(ids)-1]); !ok {
		t.Error("el trabajo más reciente no debería desalojarse")
	}
	if _, ok := svc.Get(ids[0]); ok {
		t.Error("el trabajo más antiguo debería haberse desalojado")
	}
}

func TestServiceEvictsExpiredJobsByTTL(t *testing.T) {
	cfg := fakeBins(t, "ok", 0)
	cfg.Workers = 1
	cfg.MaxJobs = 0
	cfg.JobTTL = time.Millisecond
	svc, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}

	dir := t.TempDir()
	svc.Start(context.Background())
	first := filepath.Join(dir, "viejo.ogg")
	if err := os.WriteFile(first, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	oldID, err := svc.Submit(Request{SourcePath: first, Filename: "viejo.ogg"})
	if err != nil {
		t.Fatal(err)
	}
	for range 100 {
		if snap, ok := svc.Get(oldID); ok && snap.Status == StatusDone {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	time.Sleep(10 * time.Millisecond) // pasa el TTL

	second := filepath.Join(dir, "nuevo.ogg")
	if err := os.WriteFile(second, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	newID, err := svc.Submit(Request{SourcePath: second, Filename: "nuevo.ogg"})
	if err != nil {
		t.Fatal(err)
	}
	svc.Wait()

	if _, ok := svc.Get(oldID); ok {
		t.Error("el trabajo vencido debería haberse olvidado")
	}
	if _, ok := svc.Get(newID); !ok {
		t.Error("el trabajo nuevo no debería desalojarse")
	}
}

func TestRetentionNeverDropsPendingJobs(t *testing.T) {
	// A tiny cap must not evict work that has not run yet.
	cfg := fakeBins(t, "ok", 0)
	cfg.Workers = 1
	cfg.MaxJobs = 1
	svc, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	var ids []string
	for i := range 4 {
		src := filepath.Join(dir, fmt.Sprintf("p%d.ogg", i))
		if err := os.WriteFile(src, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		id, err := svc.Submit(Request{SourcePath: src, Filename: filepath.Base(src)})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	// Nothing has been started yet: every job must still be queued.
	for _, id := range ids {
		if _, ok := svc.Get(id); !ok {
			t.Fatalf("el trabajo en cola %s se desalojó", id)
		}
	}
	svc.Start(context.Background())
	svc.Wait()
	if got := len(svc.List()); got != cfg.MaxJobs {
		t.Errorf("tras terminar quedaron %d, se esperaba %d", got, cfg.MaxJobs)
	}
}
