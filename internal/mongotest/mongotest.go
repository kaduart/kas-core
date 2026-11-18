// Package mongotest provides an isolated MongoDB database for integration
// tests. Tests run only when KAS_TEST_MONGO_URI points to a disposable
// MongoDB on a loopback address (a single-node replica set, because the
// executor uses transactions); otherwise they are skipped.
package mongotest

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

// URI returns KAS_TEST_MONGO_URI or skips the test. A non-loopback URI fails
// the test: these tests drop databases.
func URI(t testing.TB) string {
	t.Helper()
	uri := os.Getenv("KAS_TEST_MONGO_URI")
	if uri == "" {
		t.Skip("KAS_TEST_MONGO_URI is not set; skipping MongoDB integration tests")
	}
	u, err := url.Parse(uri)
	if err != nil || strings.HasPrefix(uri, "mongodb+srv://") {
		t.Fatalf("KAS_TEST_MONGO_URI must be a plain mongodb:// loopback URI")
	}
	for _, hp := range strings.Split(u.Host, ",") {
		h, _, err := net.SplitHostPort(hp)
		if err != nil {
			h = hp
		}
		if h != "localhost" {
			if ip := net.ParseIP(strings.Trim(h, "[]")); ip == nil || !ip.IsLoopback() {
				t.Fatalf("KAS_TEST_MONGO_URI must point to a loopback host, got %q", h)
			}
		}
	}
	return uri
}

var seq atomic.Int64

// Client connects to the test server; the client is closed when the test ends.
func Client(t testing.TB, opts ...*options.ClientOptions) *mongo.Client {
	t.Helper()
	o := options.Client().ApplyURI(URI(t)).SetServerSelectionTimeout(5 * time.Second)
	c, err := mongo.Connect(append([]*options.ClientOptions{o}, opts...)...)
	if err != nil {
		t.Fatalf("connect test MongoDB: %v", err)
	}
	t.Cleanup(func() { _ = c.Disconnect(context.Background()) })
	return c
}

// Database creates a unique database name and drops it when the test ends.
func Database(t testing.TB, c *mongo.Client) string {
	t.Helper()
	name := fmt.Sprintf("kas_test_%d_%d", time.Now().UnixNano(), seq.Add(1))
	t.Cleanup(func() { _ = c.Database(name).Drop(context.Background()) })
	return name
}
