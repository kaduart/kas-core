//go:build unix

package main

import "syscall"

func cpuMs() (user, sys int64) {
	var ru syscall.Rusage
	if err := syscall.Getrusage(syscall.RUSAGE_SELF, &ru); err != nil {
		return 0, 0
	}
	return ru.Utime.Sec*1000 + int64(ru.Utime.Usec)/1000, ru.Stime.Sec*1000 + int64(ru.Stime.Usec)/1000
}
