package format

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/Allrivenjs/vozgo/internal/whisper"
)

func sample() whisper.Result {
	return whisper.Result{
		Language: "es",
		Model:    "ggml-base",
		Segments: []whisper.Segment{
			{Start: 0, End: 2500 * time.Millisecond, Text: "Primera línea."},
			{Start: 2500 * time.Millisecond, End: 3661500 * time.Millisecond, Text: "Segunda línea."},
		},
	}
}

func TestRenderTXT(t *testing.T) {
	out, err := Render(sample(), Meta{}, "txt")
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(out), "Primera línea. Segunda línea.\n"; got != want {
		t.Errorf("txt = %q, want %q", got, want)
	}
}

func TestRenderSRT(t *testing.T) {
	out, err := Render(sample(), Meta{}, "srt")
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	if !strings.HasPrefix(s, "1\n00:00:00,000 --> 00:00:02,500\nPrimera línea.\n\n") {
		t.Errorf("unexpected srt head:\n%s", s)
	}
	// 3661500 ms = 1h 01m 01s 500ms, checks the hour/minute carry.
	if !strings.Contains(s, "00:00:02,500 --> 01:01:01,500") {
		t.Errorf("srt timestamp carry wrong:\n%s", s)
	}
}

func TestRenderVTT(t *testing.T) {
	out, err := Render(sample(), Meta{}, "vtt")
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	if !strings.HasPrefix(s, "WEBVTT\n\n") {
		t.Errorf("vtt must start with the WEBVTT header, got %q", s[:min(10, len(s))])
	}
	if !strings.Contains(s, "00:00:00.000 --> 00:00:02.500") {
		t.Errorf("vtt uses a dot before milliseconds:\n%s", s)
	}
}

func TestRenderJSON(t *testing.T) {
	out, err := Render(sample(), Meta{Filename: "nota.ogg", Duration: 4 * time.Second}, "json")
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Filename   string `json:"filename"`
		Language   string `json:"language"`
		DurationMS int64  `json:"duration_ms"`
		Text       string `json:"text"`
		Segments   []struct {
			StartMS int64  `json:"start_ms"`
			EndMS   int64  `json:"end_ms"`
			Text    string `json:"text"`
		} `json:"segments"`
	}
	if err := json.Unmarshal(out, &doc); err != nil {
		t.Fatalf("output is not valid JSON: %v", err)
	}
	if doc.Filename != "nota.ogg" || doc.Language != "es" || doc.DurationMS != 4000 {
		t.Errorf("bad metadata: %+v", doc)
	}
	if len(doc.Segments) != 2 || doc.Segments[1].EndMS != 3661500 {
		t.Errorf("bad segments: %+v", doc.Segments)
	}
}

func TestRenderUnknownFormat(t *testing.T) {
	if _, err := Render(sample(), Meta{}, "pdf"); err == nil {
		t.Fatal("expected an error for an unknown format")
	}
}

func TestNormalize(t *testing.T) {
	got, err := Normalize([]string{"TXT", " srt ", "txt", ""})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0] != "txt" || got[1] != "srt" {
		t.Errorf("Normalize = %v, want [txt srt]", got)
	}
	if _, err := Normalize([]string{"txt", "docx"}); err == nil {
		t.Fatal("expected an error naming the unknown format")
	}
	if _, err := Normalize(nil); err == nil {
		t.Fatal("expected an error for an empty list")
	}
}
