package httpapi

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"regexp"
	"sync/atomic"
	"time"
)

const correlationHeader = "X-Correlation-ID"
const maxCorrelationIDBytes = 64

var (
	correlationPattern  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)
	correlationFallback atomic.Uint64
)

type correlationContextKey struct{}

func CorrelationID(ctx context.Context) string {
	value, _ := ctx.Value(correlationContextKey{}).(string)
	return value
}

func validCorrelationID(value string) bool {
	return len(value) <= maxCorrelationIDBytes && correlationPattern.MatchString(value)
}

func newCorrelationID() string {
	var random [16]byte
	if _, err := rand.Read(random[:]); err == nil {
		return hex.EncodeToString(random[:])
	}
	// Correlation is diagnostic metadata only; the fallback preserves response
	// correlation if the OS random source is temporarily unavailable.
	return fmt.Sprintf("local-%x-%x", time.Now().UTC().UnixNano(), correlationFallback.Add(1))
}
