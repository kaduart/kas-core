//go:build !unix

package main

func cpuMs() (user, sys int64) { return 0, 0 }
