// Package wer mide la calidad de una transcripción contra una referencia
// humana, con la métrica estándar word error rate.
package wer

import (
	"strings"
	"unicode"
)

// Normalize deja el texto comparable: minúsculas, sin tildes y sin puntuación.
// Interesa el contenido, no la puntuación que el modelo inventa. La ñ se
// conserva porque distingue palabras (año/ano), a diferencia de un acento.
func Normalize(text string) []string {
	var b strings.Builder
	for _, r := range strings.ToLower(text) {
		switch {
		case r == 'ñ' || unicode.IsLetter(r) || unicode.IsDigit(r):
			b.WriteRune(fold(r))
		default:
			b.WriteRune(' ')
		}
	}
	return strings.Fields(b.String())
}

// fold quita la tilde de las vocales acentuadas del español.
func fold(r rune) rune {
	switch r {
	case 'á', 'à', 'ä', 'â':
		return 'a'
	case 'é', 'è', 'ë', 'ê':
		return 'e'
	case 'í', 'ì', 'ï', 'î':
		return 'i'
	case 'ó', 'ò', 'ö', 'ô':
		return 'o'
	case 'ú', 'ù', 'ü', 'û':
		return 'u'
	default:
		return r
	}
}

// Rate devuelve la distancia de edición por palabras dividida entre el largo de
// la referencia: 0 es idéntico, 1 significa tantos errores como palabras hay.
func Rate(ref, hyp []string) float64 {
	if len(ref) == 0 {
		if len(hyp) == 0 {
			return 0
		}
		return 1
	}
	// Solo se necesita la fila anterior de la matriz de Levenshtein.
	prev := make([]int, len(hyp)+1)
	cur := make([]int, len(hyp)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(ref); i++ {
		cur[0] = i
		for j := 1; j <= len(hyp); j++ {
			cost := 1
			if ref[i-1] == hyp[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
		}
		prev, cur = cur, prev
	}
	return float64(prev[len(hyp)]) / float64(len(ref))
}

// Compare normaliza ambos textos y devuelve el WER y el número de palabras de
// la referencia.
func Compare(reference, hypothesis string) (rate float64, refWords int) {
	ref := Normalize(reference)
	return Rate(ref, Normalize(hypothesis)), len(ref)
}
