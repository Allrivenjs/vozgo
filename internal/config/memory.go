package config

import (
	"os"
	"strconv"
	"strings"
)

// memoryBudget returns the bytes of RAM vozgo may assume it can use, taking the
// lowest of the cgroup limit (so a 3 GB Docker VM is respected) and the host's
// available memory. It returns 0 when nothing can be determined.
func memoryBudget() int64 {
	var budget int64
	consider := func(v int64) {
		if v > 0 && (budget == 0 || v < budget) {
			budget = v
		}
	}
	consider(cgroupLimit())
	consider(hostAvailable())
	return budget
}

// cgroupLimit reads the container memory cap, cgroup v2 first then v1.
func cgroupLimit() int64 {
	if raw, err := os.ReadFile("/sys/fs/cgroup/memory.max"); err == nil {
		s := strings.TrimSpace(string(raw))
		if s == "max" {
			return 0
		}
		if n, err := strconv.ParseInt(s, 10, 64); err == nil {
			return n
		}
	}
	if raw, err := os.ReadFile("/sys/fs/cgroup/memory/memory.limit_in_bytes"); err == nil {
		if n, err := strconv.ParseInt(strings.TrimSpace(string(raw)), 10, 64); err == nil {
			// cgroup v1 reports a huge sentinel when unlimited.
			if n < 1<<62 {
				return n
			}
		}
	}
	return 0
}

// hostAvailable reads MemAvailable from /proc/meminfo (kB).
func hostAvailable() int64 {
	raw, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return 0
	}
	for line := range strings.SplitSeq(string(raw), "\n") {
		if !strings.HasPrefix(line, "MemAvailable:") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			return 0
		}
		if n, err := strconv.ParseInt(fields[1], 10, 64); err == nil {
			return n * 1024
		}
	}
	return 0
}

// modelBytes is the size of the ggml model file, or 0 if it cannot be read.
func (c Config) modelBytes() int64 {
	st, err := os.Stat(c.ModelPath)
	if err != nil {
		return 0
	}
	return st.Size()
}

// workersByMemory caps the worker count so that concurrent whisper processes
// fit in the memory budget. Each process maps the whole model plus decoding
// state, which in practice lands near twice the model file size.
func (c Config) workersByMemory() int {
	model := c.modelBytes()
	budget := memoryBudget()
	if model == 0 || budget == 0 {
		return 0 // unknown, do not cap
	}
	perWorker := model * 2
	usable := budget * 8 / 10 // leave headroom for ffmpeg and the page cache
	n := int(usable / perWorker)
	return max(1, n)
}
