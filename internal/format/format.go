// Package format renders a transcription into the file formats vozgo can emit.
package format

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/Allrivenjs/vozgo/internal/whisper"
)

// Supported lists every format name accepted by Render.
var Supported = []string{"txt", "srt", "vtt", "json", "md"}

// Valid reports whether name is a known format.
func Valid(name string) bool {
	for _, f := range Supported {
		if f == name {
			return true
		}
	}
	return false
}

// Ext is the file extension for a format, including the dot.
func Ext(name string) string {
	switch name {
	case "md":
		return ".md"
	default:
		return "." + name
	}
}

// ContentType is the MIME type used when serving a format over HTTP.
func ContentType(name string) string {
	switch name {
	case "json":
		return "application/json; charset=utf-8"
	case "srt":
		return "application/x-subrip; charset=utf-8"
	case "vtt":
		return "text/vtt; charset=utf-8"
	case "md":
		return "text/markdown; charset=utf-8"
	default:
		return "text/plain; charset=utf-8"
	}
}

// Meta is the optional context rendered into the json and md formats.
type Meta struct {
	Filename string        `json:"filename,omitempty"`
	Duration time.Duration `json:"duration_ms,omitempty"`
	Elapsed  time.Duration `json:"elapsed_ms,omitempty"`
}

// Render converts a result into the requested format.
func Render(res whisper.Result, meta Meta, name string) ([]byte, error) {
	switch name {
	case "txt":
		return []byte(res.Text() + "\n"), nil
	case "srt":
		return []byte(renderSRT(res)), nil
	case "vtt":
		return []byte(renderVTT(res)), nil
	case "md":
		return []byte(renderMD(res, meta)), nil
	case "json":
		return renderJSON(res, meta)
	default:
		return nil, fmt.Errorf("unknown format %q (want one of %s)", name, strings.Join(Supported, ", "))
	}
}

// Normalize de-duplicates and validates a format list, preserving order.
func Normalize(formats []string) ([]string, error) {
	seen := make(map[string]bool, len(formats))
	out := make([]string, 0, len(formats))
	var bad []string
	for _, f := range formats {
		f = strings.ToLower(strings.TrimSpace(f))
		if f == "" || seen[f] {
			continue
		}
		if !Valid(f) {
			bad = append(bad, f)
			continue
		}
		seen[f] = true
		out = append(out, f)
	}
	if len(bad) > 0 {
		sort.Strings(bad)
		return nil, fmt.Errorf("unknown format(s) %s (want any of %s)",
			strings.Join(bad, ", "), strings.Join(Supported, ", "))
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no output format selected")
	}
	return out, nil
}

func renderSRT(res whisper.Result) string {
	var b strings.Builder
	for i, seg := range res.Segments {
		fmt.Fprintf(&b, "%d\n%s --> %s\n%s\n\n",
			i+1, stamp(seg.Start, ','), stamp(seg.End, ','), seg.Text)
	}
	return b.String()
}

func renderVTT(res whisper.Result) string {
	var b strings.Builder
	b.WriteString("WEBVTT\n\n")
	for _, seg := range res.Segments {
		fmt.Fprintf(&b, "%s --> %s\n%s\n\n",
			stamp(seg.Start, '.'), stamp(seg.End, '.'), seg.Text)
	}
	return b.String()
}

func renderMD(res whisper.Result, meta Meta) string {
	var b strings.Builder
	title := meta.Filename
	if title == "" {
		title = "Transcripción"
	}
	fmt.Fprintf(&b, "# %s\n\n", title)
	if res.Language != "" {
		fmt.Fprintf(&b, "- Idioma: `%s`\n", res.Language)
	}
	if res.Model != "" {
		fmt.Fprintf(&b, "- Modelo: `%s`\n", res.Model)
	}
	if meta.Duration > 0 {
		fmt.Fprintf(&b, "- Duración: %s\n", meta.Duration.Round(time.Second))
	}
	b.WriteString("\n")
	b.WriteString(res.Text())
	b.WriteString("\n")
	return b.String()
}

// jsonSegment renders durations as milliseconds so the payload stays readable
// from JavaScript, where time.Duration nanoseconds are awkward.
type jsonSegment struct {
	StartMS int64  `json:"start_ms"`
	EndMS   int64  `json:"end_ms"`
	Text    string `json:"text"`
}

type jsonDoc struct {
	Filename   string        `json:"filename,omitempty"`
	Language   string        `json:"language,omitempty"`
	Model      string        `json:"model,omitempty"`
	DurationMS int64         `json:"duration_ms,omitempty"`
	ElapsedMS  int64         `json:"elapsed_ms,omitempty"`
	Text       string        `json:"text"`
	Segments   []jsonSegment `json:"segments"`
}

func renderJSON(res whisper.Result, meta Meta) ([]byte, error) {
	doc := jsonDoc{
		Filename:   meta.Filename,
		Language:   res.Language,
		Model:      res.Model,
		DurationMS: meta.Duration.Milliseconds(),
		ElapsedMS:  meta.Elapsed.Milliseconds(),
		Text:       res.Text(),
		Segments:   make([]jsonSegment, 0, len(res.Segments)),
	}
	for _, s := range res.Segments {
		doc.Segments = append(doc.Segments, jsonSegment{
			StartMS: s.Start.Milliseconds(),
			EndMS:   s.End.Milliseconds(),
			Text:    s.Text,
		})
	}
	out, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(out, '\n'), nil
}

// stamp formats a duration as HH:MM:SS<sep>mmm.
func stamp(d time.Duration, sep byte) string {
	if d < 0 {
		d = 0
	}
	total := d.Milliseconds()
	ms := total % 1000
	total /= 1000
	s := total % 60
	total /= 60
	m := total % 60
	h := total / 60
	return fmt.Sprintf("%02d:%02d:%02d%c%03d", h, m, s, sep, ms)
}
