// kas-attribution-import sends one CSV file of KAS Attribution inputs
// (customers, contacts, receipts or refunds; columns in contract/v1/attribution.md)
// to the KAS API in batches of up to 500 lines. Batch ids are
// <batch-id>-0001, <batch-id>-0002, ...: running the same command again with
// the same file is safe (each batch returns its original answer), and the
// same file under a new batch id leaves every line "unchanged".
//
// The token is read from an environment variable, never from a flag. Plain
// http is accepted only for a loopback address.
//
//	KAS_ATTR_TOKEN=... kas-attribution-import -kas https://kas.example -service escola-sis \
//	    -token-env KAS_ATTR_TOKEN -tenant escola-demo -kind receipts -file recebimentos.csv \
//	    -batch-id rec-2026-11 -actor user.fin-07
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"time"

	"kas-core-pilot/internal/attribution"
)

func main() { os.Exit(run(os.Args[1:], os.Stdout, os.Stderr)) }

type config struct {
	base, service, token, tenant, kind, batchID, actor string
	size                                               int
}

type summary struct {
	Batches   int `json:"batches"`
	Replayed  int `json:"replayed"`
	Created   int `json:"created"`
	Updated   int `json:"updated"`
	Unchanged int `json:"unchanged"`
	Rejected  int `json:"rejected"`
	// RejectedLines lists refused lines by line number of the file (header = 1).
	RejectedLines []rejectedLine `json:"rejected_lines"`
}

type rejectedLine struct {
	Line       int    `json:"line"`
	ExternalID string `json:"external_id,omitempty"`
	Code       string `json:"code"`
}

func run(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("kas-attribution-import", flag.ContinueOnError)
	fs.SetOutput(stderr)
	base := fs.String("kas", "http://127.0.0.1:8088", "KAS base URL (https, or http on loopback)")
	service := fs.String("service", "", "service id (X-KAS-Service)")
	tokenEnv := fs.String("token-env", "", "environment variable holding the service token")
	tenant := fs.String("tenant", "", "tenant id (required when the service serves several tenants)")
	kind := fs.String("kind", "", "customers, contacts, receipts or refunds")
	file := fs.String("file", "", "CSV file (UTF-8, comma-separated, header with the contract column names)")
	batchID := fs.String("batch-id", "", "batch id prefix; batches are <batch-id>-0001, -0002, ...")
	actor := fs.String("actor", "", "who is importing: user.<ref>, service.<ref> or system.<ref>")
	size := fs.Int("batch-size", attribution.MaxBatchItems, "lines per batch (1..500)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	token, _ := os.LookupEnv(*tokenEnv)
	cfg := config{base: *base, service: *service, token: token, tenant: *tenant, kind: *kind, batchID: *batchID, actor: *actor, size: *size}
	if err := cfg.validate(); err != nil {
		fmt.Fprintln(stderr, "invalid configuration:", err)
		return 2
	}
	f, err := os.Open(*file)
	if err != nil {
		fmt.Fprintln(stderr, "cannot open file:", err)
		return 2
	}
	defer f.Close()
	items, err := attribution.ParseCSV(cfg.kind, f)
	if err != nil {
		fmt.Fprintln(stderr, "invalid CSV:", err)
		return 2
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	sum, err := send(ctx, &http.Client{Timeout: 60 * time.Second}, cfg, items)
	_ = json.NewEncoder(stdout).Encode(sum)
	if err != nil {
		fmt.Fprintln(stderr, "import stopped:", err)
		return 1
	}
	return 0
}

func (c config) validate() error {
	u, err := url.Parse(c.base)
	if err != nil || u.Host == "" {
		return errors.New("-kas must be an absolute URL")
	}
	if u.Scheme != "https" {
		ip := net.ParseIP(u.Hostname())
		if u.Scheme != "http" || !(u.Hostname() == "localhost" || (ip != nil && ip.IsLoopback())) {
			return errors.New("-kas must use https (plain http only on loopback)")
		}
	}
	switch {
	case c.service == "":
		return errors.New("-service is required")
	case c.token == "":
		return errors.New("-token-env must name a set environment variable")
	case !attribution.ValidKind(c.kind):
		return errors.New("-kind must be customers, contacts, receipts or refunds")
	case !attribution.ValidRef(c.batchID) || len(c.batchID) > 100:
		return errors.New("-batch-id must be 1-100 letters, digits, '.', '_', ':' or '-'")
	case !attribution.ValidActor(c.actor):
		return errors.New("-actor must look like user.<ref>")
	case c.size < 1 || c.size > attribution.MaxBatchItems:
		return fmt.Errorf("-batch-size must be 1..%d", attribution.MaxBatchItems)
	}
	return nil
}

type batchAnswer struct {
	attribution.BatchResult
	Duplicate bool `json:"duplicate"`
	Error     *struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// send posts the items in order and stops at the first batch the API refuses
// as a whole. Network errors are retried with backoff; a retry is safe because
// the batch id makes the import idempotent.
func send(ctx context.Context, client *http.Client, c config, items []attribution.Item) (summary, error) {
	sum := summary{RejectedLines: []rejectedLine{}}
	endpoint := c.base + "/internal/v1/attribution/" + c.kind + ":batch"
	for start, n := 0, 1; start < len(items); start, n = start+c.size, n+1 {
		end := min(start+c.size, len(items))
		body, _ := json.Marshal(map[string]any{"contract_version": attribution.ContractVersion, "tenant_id": c.tenant,
			"batch_id": fmt.Sprintf("%s-%04d", c.batchID, n), "actor": c.actor, "items": items[start:end]})
		ans, status, err := postWithRetry(ctx, client, endpoint, c, body)
		if err != nil {
			return sum, fmt.Errorf("batch %d: %w", n, err)
		}
		if status != http.StatusCreated && status != http.StatusOK {
			code := "unknown"
			if ans.Error != nil {
				code = ans.Error.Code
			}
			return sum, fmt.Errorf("batch %d refused: HTTP %d %s", n, status, code)
		}
		sum.Batches++
		if ans.Duplicate {
			sum.Replayed++
		}
		sum.Created += ans.Created
		sum.Updated += ans.Updated
		sum.Unchanged += ans.Unchanged
		for _, rj := range ans.Rejected {
			sum.Rejected++
			sum.RejectedLines = append(sum.RejectedLines, rejectedLine{Line: start + rj.Index + 2, ExternalID: rj.ExternalID, Code: rj.Code})
		}
	}
	return sum, nil
}

func postWithRetry(ctx context.Context, client *http.Client, endpoint string, c config, body []byte) (batchAnswer, int, error) {
	var lastErr error
	for attempt := 0; attempt < 4; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return batchAnswer{}, 0, ctx.Err()
			case <-time.After(time.Duration(1<<attempt) * 250 * time.Millisecond):
			}
		}
		req, _ := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-KAS-Service", c.service)
		req.Header.Set("Authorization", "Bearer "+c.token)
		resp, err := client.Do(req)
		if err != nil {
			lastErr = err
			continue
		}
		raw, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
		resp.Body.Close()
		if err != nil {
			lastErr = err
			continue
		}
		if resp.StatusCode == http.StatusServiceUnavailable || resp.StatusCode == http.StatusTooManyRequests {
			lastErr = fmt.Errorf("HTTP %d", resp.StatusCode)
			continue
		}
		var ans batchAnswer
		if err := json.Unmarshal(raw, &ans); err != nil {
			return batchAnswer{}, resp.StatusCode, fmt.Errorf("unreadable answer (HTTP %d)", resp.StatusCode)
		}
		return ans, resp.StatusCode, nil
	}
	return batchAnswer{}, 0, lastErr
}
