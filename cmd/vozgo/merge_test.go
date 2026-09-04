package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDedupeDropsIdenticalFiles(t *testing.T) {
	dir := t.TempDir()
	write := func(name, content string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	// El caso real: la misma nota descargada dos veces.
	a := write("nota.ogg", "mismo contenido")
	b := write("nota (1).ogg", "mismo contenido")
	c := write("otra.ogg", "contenido distinto")

	kept, dropped := dedupe([]string{a, b, c})
	if len(kept) != 2 || kept[0] != a || kept[1] != c {
		t.Errorf("kept = %v, se esperaba [%s %s]", kept, a, c)
	}
	if len(dropped) != 1 || dropped[0] != b {
		t.Errorf("dropped = %v, se esperaba [%s]", dropped, b)
	}
}

func TestDedupeKeepsUnreadableFiles(t *testing.T) {
	// Un archivo ilegible debe llegar a transcribirse y fallar ahí, con un
	// mensaje claro, en vez de desaparecer en silencio.
	missing := filepath.Join(t.TempDir(), "no-existe.ogg")
	kept, dropped := dedupe([]string{missing})
	if len(kept) != 1 || len(dropped) != 0 {
		t.Errorf("kept = %v, dropped = %v", kept, dropped)
	}
}

func TestDedupeEmpty(t *testing.T) {
	kept, dropped := dedupe(nil)
	if len(kept) != 0 || len(dropped) != 0 {
		t.Errorf("kept = %v, dropped = %v", kept, dropped)
	}
}

func TestHashFileDistinguishesContent(t *testing.T) {
	dir := t.TempDir()
	a := filepath.Join(dir, "a")
	b := filepath.Join(dir, "b")
	if err := os.WriteFile(a, []byte("uno"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(b, []byte("dos"), 0o644); err != nil {
		t.Fatal(err)
	}
	ha, err := hashFile(a)
	if err != nil {
		t.Fatal(err)
	}
	hb, err := hashFile(b)
	if err != nil {
		t.Fatal(err)
	}
	if ha == hb {
		t.Error("dos contenidos distintos no deberían compartir hash")
	}
	if again, _ := hashFile(a); again != ha {
		t.Error("el hash del mismo archivo debe ser estable")
	}
}

func TestCollectSortsAndDeduplicatesPaths(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"b.ogg", "a.ogg", "notas.txt"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	got, err := collect([]string{dir}, true)
	if err != nil {
		t.Fatal(err)
	}
	// Solo audio, y en orden alfabético, que en las notas de WhatsApp es el
	// orden cronológico.
	if len(got) != 2 {
		t.Fatalf("collect = %v, se esperaban 2 audios", got)
	}
	if !strings.HasSuffix(got[0], "a.ogg") || !strings.HasSuffix(got[1], "b.ogg") {
		t.Errorf("orden incorrecto: %v", got)
	}
}

func TestDedupeKeepsTheOriginalNotTheCopy(t *testing.T) {
	dir := t.TempDir()
	write := func(name string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte("igual"), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	// collect ordena alfabéticamente y el espacio va antes que el punto, así que
	// la copia " (1)" llega primero; aun así debe ganar el nombre original.
	copia := write("nota (1).ogg")
	original := write("nota.ogg")

	kept, dropped := dedupe([]string{copia, original})
	if len(kept) != 1 || kept[0] != original {
		t.Errorf("kept = %v, se esperaba [%s]", kept, original)
	}
	if len(dropped) != 1 || dropped[0] != copia {
		t.Errorf("dropped = %v, se esperaba [%s]", dropped, copia)
	}
}
