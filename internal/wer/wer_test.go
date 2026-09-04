package wer

import (
	"math"
	"slices"
	"testing"
)

func TestNormalize(t *testing.T) {
	got := Normalize("¡Hola, JAIME! ¿Qué más? Sí... el año pasado.")
	want := []string{"hola", "jaime", "que", "mas", "si", "el", "año", "pasado"}
	if !slices.Equal(got, want) {
		t.Errorf("Normalize = %q, want %q", got, want)
	}
}

func TestNormalizeKeepsEnye(t *testing.T) {
	// año y ano son palabras distintas: la ñ no es un acento que se pueda quitar.
	if got := Normalize("año ano"); !slices.Equal(got, []string{"año", "ano"}) {
		t.Errorf("Normalize = %q", got)
	}
}

func TestRateIdentical(t *testing.T) {
	words := Normalize("las preguntas van en el formato PDF")
	if got := Rate(words, words); got != 0 {
		t.Errorf("Rate de textos idénticos = %v, want 0", got)
	}
}

func TestRateCountsEachEditOnce(t *testing.T) {
	ref := Normalize("uno dos tres cuatro")
	// una sustitución, una omisión y una inserción sobre cuatro palabras
	hyp := Normalize("uno DOSCIENTOS tres extra")
	got := Rate(ref, hyp)
	if math.Abs(got-0.5) > 1e-9 {
		t.Errorf("Rate = %v, want 0.5", got)
	}
}

func TestRateEmptyReference(t *testing.T) {
	if got := Rate(nil, nil); got != 0 {
		t.Errorf("dos vacíos = %v, want 0", got)
	}
	if got := Rate(nil, Normalize("algo")); got != 1 {
		t.Errorf("referencia vacía con hipótesis = %v, want 1", got)
	}
}

func TestRateEmptyHypothesis(t *testing.T) {
	// Una transcripción vacía falla del todo, no a medias.
	ref := Normalize("una dos tres")
	if got := Rate(ref, nil); got != 1 {
		t.Errorf("hipótesis vacía = %v, want 1", got)
	}
}

func TestCompare(t *testing.T) {
	rate, words := Compare(
		"Primero es medicina laboral, luego medicina ocupacional.",
		"primero es medicina laboral luego medicina ocupacional",
	)
	if rate != 0 {
		t.Errorf("solo cambia la puntuación, WER = %v, want 0", rate)
	}
	if words != 7 {
		t.Errorf("palabras de referencia = %d, want 7", words)
	}
}
