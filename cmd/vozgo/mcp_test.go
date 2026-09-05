package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestResolveRejectsPathsOutsideRoot(t *testing.T) {
	root := t.TempDir()
	inside := filepath.Join(root, "nota.ogg")
	if err := os.WriteFile(inside, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "otra.ogg")
	if err := os.WriteFile(outside, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	m := &mcpServer{root: root}

	if _, err := m.resolve(inside); err != nil {
		t.Errorf("una ruta dentro de root debe aceptarse: %v", err)
	}
	if _, err := m.resolve(outside); err == nil {
		t.Error("una ruta fuera de root debe rechazarse")
	}
	// El clásico intento de escape con ..
	escape := filepath.Join(root, "..", filepath.Base(outside))
	if _, err := m.resolve(escape); err == nil {
		t.Error("una ruta con .. que sale de root debe rechazarse")
	}
}

func TestResolveWithoutRootAllowsAnything(t *testing.T) {
	file := filepath.Join(t.TempDir(), "n.ogg")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	m := &mcpServer{}
	if _, err := m.resolve(file); err != nil {
		t.Errorf("sin root no debe haber restricción: %v", err)
	}
}

func TestResolveRejectsMissingAndEmpty(t *testing.T) {
	m := &mcpServer{}
	if _, err := m.resolve(""); err == nil {
		t.Error("una ruta vacía debe rechazarse")
	}
	if _, err := m.resolve(filepath.Join(t.TempDir(), "no-existe.ogg")); err == nil {
		t.Error("una ruta inexistente debe rechazarse")
	}
}

func TestResolveIsRelativeToTheProcess(t *testing.T) {
	// El agente puede mandar una ruta relativa; debe volverse absoluta.
	dir := t.TempDir()
	file := filepath.Join(dir, "n.ogg")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)
	m := &mcpServer{}
	got, err := m.resolve("n.ogg")
	if err != nil {
		t.Fatal(err)
	}
	if !filepath.IsAbs(got) {
		t.Errorf("resolve devolvió una ruta relativa: %s", got)
	}
}

func TestAvailableModels(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"ggml-small.bin", "ggml-base.bin", "notas.txt"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	got := availableModels(filepath.Join(dir, "ggml-base.bin"))
	if len(got) != 2 || got[0] != "ggml-base.bin" || got[1] != "ggml-small.bin" {
		t.Errorf("availableModels = %v, se esperaban los dos .bin ordenados", got)
	}
	if strings.Contains(strings.Join(got, " "), "notas.txt") {
		t.Error("no debe listar archivos que no sean modelos")
	}
}

func TestAvailableModelsMissingDir(t *testing.T) {
	if got := availableModels("/no/existe/ggml-base.bin"); got != nil {
		t.Errorf("con un directorio inexistente debe devolver nil, dio %v", got)
	}
}
