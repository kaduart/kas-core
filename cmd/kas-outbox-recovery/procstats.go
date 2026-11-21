package main

import (
	"os"
	"runtime"
	"strconv"
	"strings"
)

// procStats is the process cost written with every shadow cycle, so the
// rehearsals measure the real memory of the Go binary (input for the hosting
// decision). RSS is what the kernel charges the process (GOMEMLIMIT is only a
// soft target for the Go heap); PeakRSS is the high-water mark since start.
type procStats struct {
	RSSBytes     uint64 `json:"rss_bytes,omitempty"`
	PeakRSSBytes uint64 `json:"peak_rss_bytes,omitempty"`
	HeapBytes    uint64 `json:"heap_bytes"`
	SysBytes     uint64 `json:"sys_bytes"`
	Goroutines   int    `json:"goroutines"`
}

func readProcStats() procStats {
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	p := procStats{HeapBytes: m.HeapAlloc, SysBytes: m.Sys, Goroutines: runtime.NumGoroutine()}
	p.RSSBytes, p.PeakRSSBytes = rss()
	return p
}

// rss reads VmRSS and VmHWM on Linux; elsewhere both are 0 (sys_bytes stays).
func rss() (cur, peak uint64) {
	b, err := os.ReadFile("/proc/self/status")
	if err != nil {
		return 0, 0
	}
	for _, line := range strings.Split(string(b), "\n") {
		f := strings.Fields(line)
		if len(f) < 2 {
			continue
		}
		kb, _ := strconv.ParseUint(f[1], 10, 64)
		switch f[0] {
		case "VmRSS:":
			cur = kb * 1024
		case "VmHWM:":
			peak = kb * 1024
		}
	}
	return cur, peak
}
