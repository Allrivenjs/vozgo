package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestValidateMissingModel(t *testing.T) {
	c := Default()
	c.ModelPath = filepath.Join(t.TempDir(), "nope.bin")
	if err := c.Validate(); err == nil {
		t.Fatal("expected an error when the model file is missing")
	}
}

func TestValidateOK(t *testing.T) {
	dir := t.TempDir()
	model := filepath.Join(dir, "ggml-base.bin")
	if err := os.WriteFile(model, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	c := Default()
	c.ModelPath = model
	c.Resolve()
	if err := c.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if c.ModelName() != "ggml-base" {
		t.Errorf("ModelName = %q, want ggml-base", c.ModelName())
	}
}

func TestApplyEnv(t *testing.T) {
	t.Setenv("VOZGO_LANGUAGE", "es")
	t.Setenv("VOZGO_WORKERS", "7")
	t.Setenv("VOZGO_FORMATS", "txt, srt")
	t.Setenv("VOZGO_THREADS", "not-a-number")

	c := Default()
	threads := c.Threads
	c.ApplyEnv()

	if c.Language != "es" {
		t.Errorf("Language = %q, want es", c.Language)
	}
	if c.Workers != 7 {
		t.Errorf("Workers = %d, want 7", c.Workers)
	}
	if len(c.Formats) != 2 || c.Formats[1] != "srt" {
		t.Errorf("Formats = %v, want [txt srt]", c.Formats)
	}
	// A malformed value must leave the default untouched instead of zeroing it.
	if c.Threads != threads {
		t.Errorf("Threads = %d, want the default %d", c.Threads, threads)
	}
}

func TestSplitFormats(t *testing.T) {
	got := SplitFormats(" txt , ,SRT ")
	if len(got) != 2 || got[0] != "txt" || got[1] != "srt" {
		t.Errorf("SplitFormats = %v, want [txt srt]", got)
	}
}

func TestResolveKeepsThreadBudgetInBounds(t *testing.T) {
	c := Default()
	c.Resolve()
	if c.Workers < 1 || c.Threads < 1 {
		t.Fatalf("Resolve left zero values: workers=%d threads=%d", c.Workers, c.Threads)
	}
	// ggml spins while waiting, so the auto values must never oversubscribe.
	if _, _, yes := c.Oversubscribed(); yes {
		t.Errorf("auto config oversubscribes: %d workers × %d threads", c.Workers, c.Threads)
	}
}

func TestResolveDerivesThreadsFromWorkers(t *testing.T) {
	c := Default()
	c.Workers = 2
	c.Resolve()
	if c.Workers != 2 {
		t.Errorf("Workers = %d, want the explicit 2", c.Workers)
	}
	if c.Threads < 1 {
		t.Errorf("Threads = %d, want >= 1", c.Threads)
	}
	if _, _, yes := c.Oversubscribed(); yes {
		t.Errorf("derived threads oversubscribe: %d × %d", c.Workers, c.Threads)
	}
}

func TestResolveRespectsExplicitValues(t *testing.T) {
	c := Default()
	c.Workers, c.Threads = 3, 5
	c.Resolve()
	if c.Workers != 3 || c.Threads != 5 {
		t.Errorf("Resolve overwrote explicit values: %d/%d", c.Workers, c.Threads)
	}
}

func TestWorkersByMemoryCapsOnBudget(t *testing.T) {
	dir := t.TempDir()
	model := filepath.Join(dir, "ggml-huge.bin")
	// A model far larger than any plausible budget must collapse to 1 worker.
	if err := os.Truncate(model, 0); err != nil {
		f, err := os.Create(model)
		if err != nil {
			t.Fatal(err)
		}
		if err := f.Truncate(200 << 30); err != nil { // 200 GiB, sparse
			t.Skipf("cannot create a sparse file here: %v", err)
		}
		f.Close()
	}
	c := Default()
	c.ModelPath = model
	if got := c.workersByMemory(); got != 1 {
		t.Errorf("workersByMemory = %d, want 1 for an oversized model", got)
	}

	c.Workers = 8
	if _, _, yes := c.MemoryTight(); !yes {
		t.Error("MemoryTight should flag 8 workers on a 200 GiB model")
	}
}

func TestWorkersByMemoryUnknownModel(t *testing.T) {
	c := Default()
	c.ModelPath = filepath.Join(t.TempDir(), "missing.bin")
	if got := c.workersByMemory(); got != 0 {
		t.Errorf("workersByMemory = %d, want 0 (unknown) when the model is missing", got)
	}
	if _, _, yes := c.MemoryTight(); yes {
		t.Error("MemoryTight must not fire when the model size is unknown")
	}
}

func TestMemoryBudgetPositive(t *testing.T) {
	// On Linux, /proc/meminfo always resolves; a zero budget would silently
	// disable the memory cap.
	if got := memoryBudget(); got <= 0 {
		t.Errorf("memoryBudget = %d, want > 0", got)
	}
}
