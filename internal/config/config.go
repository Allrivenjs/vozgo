// Package config holds the runtime configuration shared by the CLI and the HTTP API.
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// Config describes how audio is decoded and transcribed.
type Config struct {
	ModelPath  string   // path to a ggml whisper model
	Language   string   // ISO code, or "auto" to detect
	Translate  bool     // translate to English instead of transcribing
	Threads    int      // threads per whisper invocation, 0 = auto
	Workers    int      // files transcribed in parallel, 0 = auto
	BeamSize   int      // beam search width, 0 = greedy default
	Prompt     string   // initial prompt to bias decoding (dialect, jargon, names)
	PromptFile string   // file whose contents become Prompt when Prompt is empty
	Formats    []string // output formats: txt, srt, vtt, json, md
	WhisperBin string
	FFmpegBin  string
	FFprobeBin string
	TempDir    string
	KeepWAV    bool

	// MaxJobs and JobTTL bound the job history kept by `vozgo serve`.
	// 0 disables that bound. They do not affect one-shot CLI runs.
	MaxJobs int
	JobTTL  time.Duration
}

// Default returns the configuration used when no flags or env vars are set.
func Default() Config {
	return Config{
		ModelPath:  "models/ggml-base.bin",
		Language:   "auto",
		Threads:    0, // resolved by Resolve()
		Workers:    0, // resolved by Resolve()
		Formats:    []string{"txt"},
		MaxJobs:    200,
		JobTTL:     24 * time.Hour,
		WhisperBin: "whisper-cli",
		FFmpegBin:  "ffmpeg",
		FFprobeBin: "ffprobe",
		TempDir:    "",
	}
}

// ApplyEnv overlays VOZGO_* environment variables on top of the current values.
// Flags are parsed after this call, so an explicit flag always wins.
func (c *Config) ApplyEnv() {
	if v := os.Getenv("VOZGO_MODEL"); v != "" {
		c.ModelPath = v
	}
	if v := os.Getenv("VOZGO_LANGUAGE"); v != "" {
		c.Language = v
	}
	if v := os.Getenv("VOZGO_THREADS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			c.Threads = n
		}
	}
	if v := os.Getenv("VOZGO_WORKERS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			c.Workers = n
		}
	}
	if v := os.Getenv("VOZGO_FORMATS"); v != "" {
		c.Formats = SplitFormats(v)
	}
	if v := os.Getenv("VOZGO_WHISPER_BIN"); v != "" {
		c.WhisperBin = v
	}
	if v := os.Getenv("VOZGO_FFMPEG_BIN"); v != "" {
		c.FFmpegBin = v
	}
	if v := os.Getenv("VOZGO_FFPROBE_BIN"); v != "" {
		c.FFprobeBin = v
	}
	if v := os.Getenv("VOZGO_TEMP_DIR"); v != "" {
		c.TempDir = v
	}
	if v := os.Getenv("VOZGO_PROMPT"); v != "" {
		c.Prompt = v
	}
	if v := os.Getenv("VOZGO_PROMPT_FILE"); v != "" {
		c.PromptFile = v
	}
	if v := os.Getenv("VOZGO_MAX_JOBS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			c.MaxJobs = n
		}
	}
	if v := os.Getenv("VOZGO_JOB_TTL"); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d >= 0 {
			c.JobTTL = d
		}
	}
}

// SplitFormats parses a comma separated format list, dropping empty entries.
func SplitFormats(s string) []string {
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.ToLower(strings.TrimSpace(p)); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// maxPromptChars is a conservative stand-in for whisper's 224-token limit on the
// initial prompt. Beyond it the model silently drops the beginning of the text,
// so the caller warns instead of letting the bias vanish unnoticed.
const maxPromptChars = 900

// LoadPrompt reads PromptFile into Prompt. An explicit -prompt wins over the
// file, and a missing file is an error: a silently ignored prompt looks exactly
// like a model that simply did not improve.
func (c *Config) LoadPrompt() error {
	if c.PromptFile == "" {
		return nil
	}
	raw, err := os.ReadFile(c.PromptFile)
	if err != nil {
		return fmt.Errorf("no se pudo leer el prompt %q: %w", c.PromptFile, err)
	}
	text := strings.Join(strings.Fields(string(raw)), " ")
	if text == "" {
		return fmt.Errorf("el archivo de prompt %q está vacío", c.PromptFile)
	}
	if c.Prompt == "" {
		c.Prompt = text
	}
	return nil
}

// PromptTooLong reports whether the prompt is likely to be truncated by whisper.
func (c Config) PromptTooLong() (chars, limit int, yes bool) {
	return len(c.Prompt), maxPromptChars, len(c.Prompt) > maxPromptChars
}

// Resolve fills in the automatic (zero) values for Workers and Threads.
//
// ggml busy-waits between threads, so running more whisper threads than the
// machine has logical CPUs collapses throughput instead of improving it: two
// workers at 16 threads each on a 16-thread host spin at ~1800% CPU and take
// minutes on audio that finishes in seconds at 4 threads. So the total thread
// budget across workers is kept at or below NumCPU.
func (c *Config) Resolve() {
	ncpu := max(1, runtime.NumCPU())
	if c.Workers <= 0 {
		c.Workers = min(4, max(1, ncpu/4))
		// Each worker is a separate whisper process holding its own copy of
		// the model, so RAM — not CPU — is usually the binding constraint.
		if byMem := c.workersByMemory(); byMem > 0 {
			c.Workers = min(c.Workers, byMem)
		}
	}
	if c.Threads <= 0 {
		c.Threads = max(1, ncpu/c.Workers)
	}
}

// Oversubscribed reports whether Workers*Threads exceeds the logical CPU count.
// Explicit flags are honoured, but the caller warns so a mystery slowdown is
// traceable.
func (c Config) Oversubscribed() (total, ncpu int, yes bool) {
	ncpu = max(1, runtime.NumCPU())
	total = c.Workers * c.Threads
	return total, ncpu, total > ncpu
}

// MemoryTight reports whether the configured worker count is likely to exhaust
// the memory budget (cgroup limit or host available memory). The caller warns:
// an OOM kill inside Docker is otherwise a silent, confusing failure.
func (c Config) MemoryTight() (needBytes, budgetBytes int64, yes bool) {
	model := c.modelBytes()
	budget := memoryBudget()
	if model == 0 || budget == 0 {
		return 0, 0, false
	}
	need := int64(c.Workers) * model * 2
	return need, budget, need > budget*8/10
}

// Validate checks that the model exists and that every requested format is known.
// It does not check the binaries; those are resolved lazily so that `vozgo version`
// works on a machine without ffmpeg.
func (c Config) Validate() error {
	if c.ModelPath == "" {
		return fmt.Errorf("no model configured: pass -model or set VOZGO_MODEL")
	}
	st, err := os.Stat(c.ModelPath)
	if err != nil {
		return fmt.Errorf("model %q not found: %w (run scripts/download-model.sh)", c.ModelPath, err)
	}
	if st.IsDir() {
		return fmt.Errorf("model %q is a directory, expected a .bin file", c.ModelPath)
	}
	if len(c.Formats) == 0 {
		return fmt.Errorf("no output format selected")
	}
	if c.Threads < 1 {
		return fmt.Errorf("threads must be >= 1, got %d", c.Threads)
	}
	if c.Workers < 1 {
		return fmt.Errorf("workers must be >= 1, got %d", c.Workers)
	}
	return nil
}

// ModelName is the model file name without extension, for logs and API payloads.
func (c Config) ModelName() string {
	return strings.TrimSuffix(filepath.Base(c.ModelPath), filepath.Ext(c.ModelPath))
}
