package main

import (
	"os"
	"runtime"
	"strconv"
	"strings"
)

// procStats is the process cost recorded with every shadow cycle, so the
// five-day comparison can show memory and CPU without an external agent.
type procStats struct {
	RSSBytes   uint64 `json:"rss_bytes,omitempty"`
	HeapBytes  uint64 `json:"heap_bytes"`
	SysBytes   uint64 `json:"sys_bytes"`
	Goroutines int    `json:"goroutines"`
	CPUUserMs  int64  `json:"cpu_user_ms,omitempty"`
	CPUSysMs   int64  `json:"cpu_sys_ms,omitempty"`
}

func readProcStats() procStats {
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	p := procStats{HeapBytes: m.HeapAlloc, SysBytes: m.Sys, Goroutines: runtime.NumGoroutine()}
	p.RSSBytes = rssBytes()
	p.CPUUserMs, p.CPUSysMs = cpuMs()
	return p
}

// rssBytes reads VmRSS on Linux; elsewhere it returns 0 (sys_bytes stays).
func rssBytes() uint64 {
	b, err := os.ReadFile("/proc/self/status")
	if err != nil {
		return 0
	}
	for _, line := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(line, "VmRSS:") {
			f := strings.Fields(line)
			if len(f) >= 2 {
				kb, _ := strconv.ParseUint(f[1], 10, 64)
				return kb * 1024
			}
		}
	}
	return 0
}
