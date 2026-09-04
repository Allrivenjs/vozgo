package whisper

import (
	"testing"
	"time"
)

const sampleJSON = `{
  "systeminfo": "AVX = 1",
  "model": {"type": "base"},
  "params": {"model": "models/ggml-base.bin", "language": "es", "translate": false},
  "result": {"language": "es"},
  "transcription": [
    {"timestamps": {"from": "00:00:00,000", "to": "00:00:03,500"},
     "offsets": {"from": 0, "to": 3500}, "text": " Hola, esto es una prueba."},
    {"timestamps": {"from": "00:00:03,500", "to": "00:00:05,000"},
     "offsets": {"from": 3500, "to": 5000}, "text": " [BLANK_AUDIO]"},
    {"timestamps": {"from": "00:00:05,000", "to": "00:00:07,250"},
     "offsets": {"from": 5000, "to": 7250}, "text": " Segunda parte."}
  ]
}`

func TestParseJSON(t *testing.T) {
	res, err := parseJSON([]byte(sampleJSON))
	if err != nil {
		t.Fatalf("parseJSON: %v", err)
	}
	if res.Language != "es" {
		t.Errorf("language = %q, want es", res.Language)
	}
	// The blank-audio marker carries no speech and must be dropped.
	if len(res.Segments) != 2 {
		t.Fatalf("segments = %d, want 2: %+v", len(res.Segments), res.Segments)
	}
	if got, want := res.Segments[0].Start, time.Duration(0); got != want {
		t.Errorf("start = %v, want %v", got, want)
	}
	if got, want := res.Segments[0].End, 3500*time.Millisecond; got != want {
		t.Errorf("end = %v, want %v", got, want)
	}
	if got, want := res.Segments[0].Text, "Hola, esto es una prueba."; got != want {
		t.Errorf("text = %q, want %q", got, want)
	}
	if got, want := res.Text(), "Hola, esto es una prueba. Segunda parte."; got != want {
		t.Errorf("Text() = %q, want %q", got, want)
	}
}

func TestParseJSONInvalid(t *testing.T) {
	if _, err := parseJSON([]byte("not json")); err == nil {
		t.Fatal("expected an error for malformed JSON")
	}
}
