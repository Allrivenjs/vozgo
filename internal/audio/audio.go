// Package audio decodes arbitrary input files into the 16 kHz mono PCM WAV that
// whisper.cpp requires. WhatsApp voice notes are Opus in an Ogg container, which
// whisper.cpp cannot read directly.
package audio

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// Extensions are the input types vozgo picks up when walking a directory.
var Extensions = []string{
	".ogg", ".opus", ".oga", ".m4a", ".mp3", ".wav", ".aac", ".amr",
	".flac", ".wma", ".mp4", ".mkv", ".webm", ".3gp",
}

// IsSupported reports whether ext (lowercase, with dot) is in Extensions.
func IsSupported(ext string) bool {
	ext = strings.ToLower(ext)
	for _, e := range Extensions {
		if e == ext {
			return true
		}
	}
	return false
}

// Info is the subset of ffprobe metadata vozgo reports back to the user.
type Info struct {
	Duration time.Duration
	Codec    string
}

// Converter shells out to ffmpeg/ffprobe.
type Converter struct {
	FFmpegBin  string
	FFprobeBin string
}

// ToWAV decodes the first audio stream of src into dst as 16 kHz mono signed
// 16-bit PCM. dst is overwritten if it exists.
func (c Converter) ToWAV(ctx context.Context, src, dst string) error {
	bin := c.FFmpegBin
	if bin == "" {
		bin = "ffmpeg"
	}
	args := []string{
		"-hide_banner", "-loglevel", "error", "-nostdin", "-y",
		"-i", src,
		"-vn", "-map", "0:a:0",
		"-ac", "1", "-ar", "16000",
		"-c:a", "pcm_s16le", "-f", "wav",
		dst,
	}
	var stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if errors.Is(ctx.Err(), context.Canceled) {
			return ctx.Err()
		}
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return fmt.Errorf("ffmpeg failed on %s: %s", src, firstLines(msg, 3))
	}
	return nil
}

// Probe returns the duration and codec of src. Failures are the caller's to
// ignore: metadata is informational, transcription does not depend on it.
func (c Converter) Probe(ctx context.Context, src string) (Info, error) {
	bin := c.FFprobeBin
	if bin == "" {
		bin = "ffprobe"
	}
	cmd := exec.CommandContext(ctx, bin,
		"-v", "error",
		"-select_streams", "a:0",
		"-show_entries", "stream=codec_name",
		"-show_entries", "format=duration",
		"-of", "default=noprint_wrappers=1:nokey=0",
		src,
	)
	out, err := cmd.Output()
	if err != nil {
		return Info{}, fmt.Errorf("ffprobe failed on %s: %w", src, err)
	}
	var info Info
	for line := range strings.SplitSeq(strings.TrimSpace(string(out)), "\n") {
		key, val, ok := strings.Cut(strings.TrimSpace(line), "=")
		if !ok {
			continue
		}
		switch key {
		case "codec_name":
			info.Codec = val
		case "duration":
			if secs, err := strconv.ParseFloat(val, 64); err == nil {
				info.Duration = time.Duration(secs * float64(time.Second))
			}
		}
	}
	return info, nil
}

func firstLines(s string, n int) string {
	lines := strings.Split(s, "\n")
	if len(lines) > n {
		lines = lines[:n]
	}
	return strings.Join(lines, "; ")
}
