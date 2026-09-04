package audio

import "testing"

func TestIsSupported(t *testing.T) {
	for _, ext := range []string{".ogg", ".OGG", ".opus", ".m4a", ".wav"} {
		if !IsSupported(ext) {
			t.Errorf("IsSupported(%q) = false, want true", ext)
		}
	}
	for _, ext := range []string{".txt", ".pdf", "", ".ogg.part"} {
		if IsSupported(ext) {
			t.Errorf("IsSupported(%q) = true, want false", ext)
		}
	}
}
