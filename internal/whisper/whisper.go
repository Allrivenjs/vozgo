// Package whisper drives the whisper.cpp `whisper-cli` binary and parses its
// JSON output into segments. Shelling out (instead of using cgo bindings) keeps
// vozgo a pure-Go, CGO_ENABLED=0 binary while whisper.cpp is built separately
// for CPU or CUDA inside the image.
package whisper

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// Segment is one timestamped chunk of transcribed speech. It is an internal
// value: the package/format renders the wire formats, so there are no JSON tags
// here to imply a serialization contract this type does not have.
type Segment struct {
	Start time.Duration
	End   time.Duration
	Text  string
}

// Result is the full transcription of a single audio file.
type Result struct {
	Language string
	Model    string
	Segments []Segment
}

// Text joins every segment into a single paragraph.
func (r Result) Text() string {
	var b strings.Builder
	for i, s := range r.Segments {
		t := strings.TrimSpace(s.Text)
		if t == "" {
			continue
		}
		if i > 0 && b.Len() > 0 {
			b.WriteByte(' ')
		}
		b.WriteString(t)
	}
	return b.String()
}

// Runner invokes whisper-cli with a fixed model and decoding settings.
type Runner struct {
	Bin       string
	ModelPath string
	Language  string
	Translate bool
	Threads   int
	BeamSize  int
	Prompt    string
}

// Transcribe runs whisper-cli over a 16 kHz mono WAV file. workDir holds the
// intermediate JSON that whisper-cli writes; the caller owns its lifetime.
func (r Runner) Transcribe(ctx context.Context, wavPath, workDir string) (Result, error) {
	bin := r.Bin
	if bin == "" {
		bin = "whisper-cli"
	}
	outBase := filepath.Join(workDir, "transcript")
	lang := r.Language
	if lang == "" {
		lang = "auto"
	}

	args := []string{
		"-m", r.ModelPath,
		"-f", wavPath,
		"-l", lang,
		"-oj",
		"-of", outBase,
		"-np", // no progress prints, keeps stderr useful for real errors
	}
	if r.Threads > 0 {
		args = append(args, "-t", strconv.Itoa(r.Threads))
	}
	if r.BeamSize > 0 {
		args = append(args, "-bs", strconv.Itoa(r.BeamSize))
	}
	if r.Translate {
		args = append(args, "-tr")
	}
	if r.Prompt != "" {
		args = append(args, "--prompt", r.Prompt)
	}

	var stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return Result{}, ctx.Err()
		}
		// Quedarse sin memoria es el fallo más común con modelos grandes, y
		// whisper-cli muere sin decir nada: su última línea habla del audio, lo
		// que manda a depurar en la dirección equivocada.
		if wasKilled(cmd) {
			return Result{}, fmt.Errorf(
				"whisper-cli fue terminado por el sistema, casi siempre por falta de memoria: "+
					"el modelo %s necesita ~3× su tamaño en RAM. Usa un modelo menor o dale más memoria al contenedor",
				filepath.Base(r.ModelPath))
		}
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return Result{}, fmt.Errorf("whisper-cli failed: %s", lastLines(msg, 4))
	}

	jsonPath := outBase + ".json"
	raw, err := os.ReadFile(jsonPath)
	if err != nil {
		return Result{}, fmt.Errorf("reading whisper output: %w", err)
	}
	res, err := parseJSON(raw)
	if err != nil {
		return Result{}, err
	}
	res.Model = strings.TrimSuffix(filepath.Base(r.ModelPath), filepath.Ext(r.ModelPath))
	return res, nil
}

// cliOutput mirrors the shape written by whisper.cpp's -oj flag.
type cliOutput struct {
	Result struct {
		Language string `json:"language"`
	} `json:"result"`
	Transcription []struct {
		Offsets struct {
			From int64 `json:"from"`
			To   int64 `json:"to"`
		} `json:"offsets"`
		Text string `json:"text"`
	} `json:"transcription"`
}

func parseJSON(raw []byte) (Result, error) {
	var out cliOutput
	if err := json.Unmarshal(raw, &out); err != nil {
		return Result{}, fmt.Errorf("parsing whisper JSON: %w", err)
	}
	res := Result{
		Language: out.Result.Language,
		Segments: make([]Segment, 0, len(out.Transcription)),
	}
	for _, seg := range out.Transcription {
		text := strings.TrimSpace(seg.Text)
		if text == "" || text == "[BLANK_AUDIO]" {
			continue
		}
		res.Segments = append(res.Segments, Segment{
			Start: time.Duration(seg.Offsets.From) * time.Millisecond,
			End:   time.Duration(seg.Offsets.To) * time.Millisecond,
			Text:  text,
		})
	}
	return res, nil
}

// wasKilled reporta si el proceso murió por SIGKILL, que es lo que hace el OOM
// killer. Docker lo traduce a estado de salida 137.
func wasKilled(cmd *exec.Cmd) bool {
	state := cmd.ProcessState
	if state == nil {
		return false
	}
	if status, ok := state.Sys().(syscall.WaitStatus); ok {
		return status.Signaled() && status.Signal() == syscall.SIGKILL
	}
	return state.ExitCode() == 137
}

func lastLines(s string, n int) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "; ")
}
