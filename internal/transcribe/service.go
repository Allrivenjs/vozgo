// Package transcribe owns the job queue: it converts each input to WAV, runs
// whisper, renders the requested formats, and tracks state for both the CLI and
// the HTTP API.
package transcribe

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/Allrivenjs/vozgo/internal/audio"
	"github.com/Allrivenjs/vozgo/internal/config"
	"github.com/Allrivenjs/vozgo/internal/format"
	"github.com/Allrivenjs/vozgo/internal/whisper"
)

// Status is the lifecycle state of a job.
type Status string

const (
	StatusQueued  Status = "queued"
	StatusRunning Status = "running"
	StatusDone    Status = "done"
	StatusFailed  Status = "failed"
)

// Request describes one file to transcribe.
type Request struct {
	// SourcePath is the file on disk to read. For uploads this is a temp file.
	SourcePath string
	// Filename is the display name shown to the user (upload name or basename).
	Filename string
	// OutputDir, when set, is where rendered files are written. Empty means the
	// result is only kept in memory, which is what the HTTP API does.
	OutputDir string
	// DeleteSource removes SourcePath once the job finishes. Set for uploads.
	DeleteSource bool
}

// Job is the tracked state of a Request. Fields are guarded by Service.mu;
// read them through Snapshot.
type Job struct {
	ID       string
	Filename string
	Status   Status
	Err      string

	Duration time.Duration // audio length, from ffprobe
	Elapsed  time.Duration // wall clock spent transcribing

	Result  *whisper.Result
	Outputs map[string]string // format -> written file path

	QueuedAt   time.Time
	StartedAt  time.Time
	FinishedAt time.Time

	req Request
}

// finished reports whether the job reached a terminal state. Caller must hold
// the service lock.
func (j *Job) finished() bool {
	return j.Status == StatusDone || j.Status == StatusFailed
}

// Snapshot is an immutable copy of a Job, safe to hand to another goroutine.
type Snapshot struct {
	ID         string            `json:"id"`
	Filename   string            `json:"filename"`
	Status     Status            `json:"status"`
	Err        string            `json:"error,omitempty"`
	Language   string            `json:"language,omitempty"`
	Model      string            `json:"model,omitempty"`
	DurationMS int64             `json:"duration_ms,omitempty"`
	ElapsedMS  int64             `json:"elapsed_ms,omitempty"`
	Text       string            `json:"text,omitempty"`
	Segments   int               `json:"segments"`
	Outputs    map[string]string `json:"outputs,omitempty"`
	QueuedAt   time.Time         `json:"queued_at"`
	FinishedAt *time.Time        `json:"finished_at,omitempty"`
}

func (j *Job) snapshot() Snapshot {
	s := Snapshot{
		ID:         j.ID,
		Filename:   j.Filename,
		Status:     j.Status,
		Err:        j.Err,
		DurationMS: j.Duration.Milliseconds(),
		ElapsedMS:  j.Elapsed.Milliseconds(),
		QueuedAt:   j.QueuedAt,
	}
	if !j.FinishedAt.IsZero() {
		t := j.FinishedAt
		s.FinishedAt = &t
	}
	if j.Result != nil {
		s.Language = j.Result.Language
		s.Model = j.Result.Model
		s.Text = j.Result.Text()
		s.Segments = len(j.Result.Segments)
	}
	if len(j.Outputs) > 0 {
		s.Outputs = make(map[string]string, len(j.Outputs))
		for k, v := range j.Outputs {
			s.Outputs[k] = v
		}
	}
	return s
}

// Service runs a fixed pool of workers over a job queue.
type Service struct {
	cfg     config.Config
	conv    audio.Converter
	runner  whisper.Runner
	formats []string

	queue chan *Job
	wg    sync.WaitGroup

	// Retention bounds the in-memory job history of a long-running server:
	// without it, `vozgo serve` grows for as long as people keep uploading.
	maxJobs int
	jobTTL  time.Duration

	mu    sync.RWMutex
	jobs  map[string]*Job
	order []string

	started   bool
	closeOnce sync.Once

	// OnEvent, when set, is called on every status transition. Used by the CLI
	// to print progress. It must not block for long.
	OnEvent func(Snapshot)

	subsMu sync.Mutex
	subs   map[chan Snapshot]struct{}
}

// New validates the config and builds a Service. Call Start before Submit.
func New(cfg config.Config) (*Service, error) {
	cfg.Resolve()
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	formats, err := format.Normalize(cfg.Formats)
	if err != nil {
		return nil, err
	}
	return &Service{
		cfg:  cfg,
		conv: audio.Converter{FFmpegBin: cfg.FFmpegBin, FFprobeBin: cfg.FFprobeBin},
		runner: whisper.Runner{
			Bin:       cfg.WhisperBin,
			ModelPath: cfg.ModelPath,
			Language:  cfg.Language,
			Translate: cfg.Translate,
			Threads:   cfg.Threads,
			BeamSize:  cfg.BeamSize,
			Prompt:    cfg.Prompt,
		},
		formats: formats,
		queue:   make(chan *Job, 256),
		jobs:    make(map[string]*Job),
		maxJobs: cfg.MaxJobs,
		jobTTL:  cfg.JobTTL,
	}, nil
}

// Subscribe returns a channel of status transitions and a function to stop
// listening. The HTTP layer uses it to push updates over SSE instead of having
// the browser poll.
//
// Sends are non-blocking: a subscriber that falls behind loses events rather
// than stalling a worker. Losing one is harmless because every event carries
// the job's full state, and the UI refetches the list on any event.
func (s *Service) Subscribe(buffer int) (<-chan Snapshot, func()) {
	if buffer <= 0 {
		buffer = 32
	}
	ch := make(chan Snapshot, buffer)
	s.subsMu.Lock()
	if s.subs == nil {
		s.subs = make(map[chan Snapshot]struct{})
	}
	s.subs[ch] = struct{}{}
	s.subsMu.Unlock()

	var once sync.Once
	cancel := func() {
		once.Do(func() {
			s.subsMu.Lock()
			delete(s.subs, ch)
			s.subsMu.Unlock()
			close(ch)
		})
	}
	return ch, cancel
}

// Subscribers reports how many SSE listeners are attached, for tests and health.
func (s *Service) Subscribers() int {
	s.subsMu.Lock()
	defer s.subsMu.Unlock()
	return len(s.subs)
}

// Config returns the effective configuration, for /healthz and logs.
func (s *Service) Config() config.Config { return s.cfg }

// Formats returns the normalized output format list.
func (s *Service) Formats() []string { return s.formats }

// Start launches cfg.Workers goroutines. Cancelling ctx aborts in-flight
// ffmpeg/whisper processes.
func (s *Service) Start(ctx context.Context) {
	s.mu.Lock()
	if s.started {
		s.mu.Unlock()
		return
	}
	s.started = true
	s.mu.Unlock()

	for range s.cfg.Workers {
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			for job := range s.queue {
				s.process(ctx, job)
			}
		}()
	}
}

// Submit enqueues a request and returns the job id.
func (s *Service) Submit(req Request) (string, error) {
	if req.SourcePath == "" {
		return "", errors.New("empty source path")
	}
	if req.Filename == "" {
		req.Filename = filepath.Base(req.SourcePath)
	}
	job := &Job{
		ID:       newID(),
		Filename: req.Filename,
		Status:   StatusQueued,
		QueuedAt: time.Now(),
		req:      req,
	}
	s.mu.Lock()
	s.jobs[job.ID] = job
	s.order = append(s.order, job.ID)
	s.prune()
	s.mu.Unlock()

	s.emit(job)
	s.queue <- job
	return job.ID, nil
}

// Get returns a snapshot of one job.
func (s *Service) Get(id string) (Snapshot, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	job, ok := s.jobs[id]
	if !ok {
		return Snapshot{}, false
	}
	return job.snapshot(), true
}

// Render produces the given format for a finished job.
func (s *Service) Render(id, formatName string) ([]byte, error) {
	s.mu.RLock()
	job, ok := s.jobs[id]
	if !ok {
		s.mu.RUnlock()
		return nil, fmt.Errorf("job %s not found", id)
	}
	if job.Status != StatusDone || job.Result == nil {
		st := job.Status
		s.mu.RUnlock()
		return nil, fmt.Errorf("job %s is %s, not done", id, st)
	}
	res := *job.Result
	meta := format.Meta{Filename: job.Filename, Duration: job.Duration, Elapsed: job.Elapsed}
	s.mu.RUnlock()
	return format.Render(res, meta, formatName)
}

// List returns snapshots newest first.
func (s *Service) List() []Snapshot {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Snapshot, 0, len(s.order))
	for i := len(s.order) - 1; i >= 0; i-- {
		if job, ok := s.jobs[s.order[i]]; ok {
			out = append(out, job.snapshot())
		}
	}
	return out
}

// Delete forgets a job. Returns false if the id is unknown.
func (s *Service) Delete(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.jobs[id]; !ok {
		return false
	}
	delete(s.jobs, id)
	for i, v := range s.order {
		if v == id {
			s.order = append(s.order[:i], s.order[i+1:]...)
			break
		}
	}
	return true
}

// Wait closes the queue and blocks until every worker drains. Submitting after
// Wait panics, so the CLI calls it once, after enqueuing everything.
func (s *Service) Wait() {
	s.closeOnce.Do(func() { close(s.queue) })
	s.wg.Wait()
}

// Stats summarizes the queue, used for CLI exit codes and the API summary.
type Stats struct {
	Total  int `json:"total"`
	Done   int `json:"done"`
	Failed int `json:"failed"`
}

// Stats counts jobs by terminal state.
func (s *Service) Stats() Stats {
	s.mu.RLock()
	defer s.mu.RUnlock()
	st := Stats{Total: len(s.jobs)}
	for _, j := range s.jobs {
		switch j.Status {
		case StatusDone:
			st.Done++
		case StatusFailed:
			st.Failed++
		}
	}
	return st
}

func (s *Service) process(ctx context.Context, job *Job) {
	start := time.Now()
	s.mu.Lock()
	job.Status = StatusRunning
	job.StartedAt = start
	req := job.req
	s.mu.Unlock()
	s.emit(job)

	res, dur, err := s.run(ctx, req)

	s.mu.Lock()
	job.Duration = dur
	job.Elapsed = time.Since(start)
	job.FinishedAt = time.Now()
	if err != nil {
		job.Status = StatusFailed
		job.Err = err.Error()
	} else {
		job.Status = StatusDone
		job.Result = &res
	}
	s.mu.Unlock()

	if err == nil && req.OutputDir != "" {
		if paths, werr := s.writeOutputs(job, req, res); werr != nil {
			s.mu.Lock()
			job.Status = StatusFailed
			job.Err = werr.Error()
			s.mu.Unlock()
		} else {
			s.mu.Lock()
			job.Outputs = paths
			s.mu.Unlock()
		}
	}

	if req.DeleteSource {
		_ = os.Remove(req.SourcePath)
	}

	// Also prune here, not just on Submit: a server that goes quiet after a
	// burst should release the history instead of holding it until the next
	// upload.
	s.mu.Lock()
	s.prune()
	s.mu.Unlock()

	s.emit(job)
}

// run does the actual work for one request: probe, decode, transcribe.
func (s *Service) run(ctx context.Context, req Request) (whisper.Result, time.Duration, error) {
	workDir, err := os.MkdirTemp(s.cfg.TempDir, "vozgo-")
	if err != nil {
		return whisper.Result{}, 0, fmt.Errorf("creating temp dir: %w", err)
	}
	if !s.cfg.KeepWAV {
		defer os.RemoveAll(workDir)
	}

	// Duration is informational; a probe failure must not fail the job.
	var dur time.Duration
	if info, perr := s.conv.Probe(ctx, req.SourcePath); perr == nil {
		dur = info.Duration
	}

	wav := filepath.Join(workDir, "audio.wav")
	if err := s.conv.ToWAV(ctx, req.SourcePath, wav); err != nil {
		return whisper.Result{}, dur, err
	}
	res, err := s.runner.Transcribe(ctx, wav, workDir)
	if err != nil {
		return whisper.Result{}, dur, err
	}
	return res, dur, nil
}

func (s *Service) writeOutputs(job *Job, req Request, res whisper.Result) (map[string]string, error) {
	if err := os.MkdirAll(req.OutputDir, 0o755); err != nil {
		return nil, fmt.Errorf("creating output dir: %w", err)
	}
	base := strings.TrimSuffix(filepath.Base(req.Filename), filepath.Ext(req.Filename))
	if base == "" {
		base = job.ID
	}
	s.mu.RLock()
	meta := format.Meta{Filename: job.Filename, Duration: job.Duration, Elapsed: job.Elapsed}
	s.mu.RUnlock()

	paths := make(map[string]string, len(s.formats))
	for _, f := range s.formats {
		data, err := format.Render(res, meta, f)
		if err != nil {
			return nil, err
		}
		path := filepath.Join(req.OutputDir, base+format.Ext(f))
		if err := os.WriteFile(path, data, 0o644); err != nil {
			return nil, fmt.Errorf("writing %s: %w", path, err)
		}
		paths[f] = path
	}
	return paths, nil
}

// prune drops finished jobs that are older than the TTL, then the oldest
// finished jobs above the count cap. Queued and running jobs are never dropped.
// Caller must hold s.mu.
func (s *Service) prune() {
	if s.jobTTL <= 0 && s.maxJobs <= 0 {
		return
	}
	now := time.Now()
	kept := s.order[:0]
	for _, id := range s.order {
		job, ok := s.jobs[id]
		if !ok {
			continue
		}
		expired := s.jobTTL > 0 && job.finished() &&
			!job.FinishedAt.IsZero() && now.Sub(job.FinishedAt) > s.jobTTL
		if expired {
			delete(s.jobs, id)
			continue
		}
		kept = append(kept, id)
	}
	s.order = kept

	if s.maxJobs <= 0 || len(s.order) <= s.maxJobs {
		return
	}
	// Walk oldest first, evicting finished jobs until the count fits.
	excess := len(s.order) - s.maxJobs
	kept = make([]string, 0, len(s.order))
	for _, id := range s.order {
		job, ok := s.jobs[id]
		if !ok {
			continue
		}
		if excess > 0 && job.finished() {
			delete(s.jobs, id)
			excess--
			continue
		}
		kept = append(kept, id)
	}
	s.order = kept
}

func (s *Service) emit(job *Job) {
	s.subsMu.Lock()
	hasSubs := len(s.subs) > 0
	s.subsMu.Unlock()
	if s.OnEvent == nil && !hasSubs {
		return
	}

	s.mu.RLock()
	snap := job.snapshot()
	s.mu.RUnlock()

	if s.OnEvent != nil {
		s.OnEvent(snap)
	}

	s.subsMu.Lock()
	defer s.subsMu.Unlock()
	for ch := range s.subs {
		select {
		case ch <- snap:
		default: // subscriber behind; drop rather than block the worker
		}
	}
}

func newID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b[:])
}
