package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

// The shadow report: one start line, then one line per cycle with an item per
// scanned failed entry (type, attempts, age, proposed destination), and the
// database is left exactly as it was. Disposable MongoDB, fictitious data.
func TestShadowReportOnADisposableDatabase(t *testing.T) {
	uri := os.Getenv("KAS_TEST_MONGO_URI")
	if uri == "" {
		t.Skip("KAS_TEST_MONGO_URI not set")
	}
	ctx := context.Background()
	client, err := mongo.Connect(options.Client().ApplyURI(uri))
	if err != nil {
		t.Fatal(err)
	}
	defer client.Disconnect(ctx)
	db := "outbox_shadow_report_" + time.Now().Format("150405.000000")
	db = strings.ReplaceAll(db, ".", "_")
	coll := client.Database(db).Collection("outboxes")
	defer client.Database(db).Drop(ctx)
	now := time.Now()
	docs := []any{
		bson.D{{Key: "eventId", Value: "a"}, {Key: "eventType", Value: "APPOINTMENT_CREATED"}, {Key: "payload", Value: bson.D{{Key: "nome", Value: "PACIENTE-FICTICIO"}}},
			{Key: "status", Value: "failed"}, {Key: "attempts", Value: 2}, {Key: "lastError", Value: "Connection is closed."},
			{Key: "scheduledAt", Value: now.Add(-time.Minute)}, {Key: "createdAt", Value: now.Add(-3 * time.Hour)}},
		bson.D{{Key: "eventId", Value: "b"}, {Key: "eventType", Value: "APPOINTMENT_CONFIRMED"}, {Key: "payload", Value: bson.D{}},
			{Key: "status", Value: "failed"}, {Key: "attempts", Value: 1}, {Key: "lastError", Value: "UNKNOWN_EVENT_TYPE: APPOINTMENT_CONFIRMED não está em eventToQueueMap"},
			{Key: "scheduledAt", Value: now.Add(-time.Minute)}, {Key: "createdAt", Value: now.Add(-50 * time.Hour)}},
		bson.D{{Key: "eventId", Value: "c"}, {Key: "eventType", Value: "PATIENT_UPDATED"}, {Key: "payload", Value: bson.D{}},
			{Key: "status", Value: "failed"}, {Key: "attempts", Value: 5}, {Key: "lastError", Value: "x"},
			{Key: "scheduledAt", Value: now.Add(-time.Minute)}, {Key: "createdAt", Value: now.Add(-10 * 24 * time.Hour)}},
		bson.D{{Key: "eventId", Value: "d"}, {Key: "eventType", Value: "PATIENT_UPDATED"}, {Key: "payload", Value: bson.D{}},
			{Key: "status", Value: "published"}, {Key: "attempts", Value: 0}, {Key: "createdAt", Value: now}},
	}
	if _, err := coll.InsertMany(ctx, docs); err != nil {
		t.Fatal(err)
	}
	before, _ := coll.Find(ctx, bson.D{})
	var b0 []bson.M
	_ = before.All(ctx, &b0)

	rep := filepath.Join(t.TempDir(), "r.jsonl")
	t.Setenv("KAS_OUTBOX_MONGO_URI", uri)
	var out, errb bytes.Buffer
	if code := run(ctx, []string{"-db", db, "-once", "-report", rep}, &out, &errb); code != 0 {
		t.Fatalf("exit %d: %s %s", code, out.String(), errb.String())
	}
	f, _ := os.Open(rep)
	defer f.Close()
	sc := bufio.NewScanner(f)
	var lines []map[string]any
	for sc.Scan() {
		var m map[string]any
		if err := json.Unmarshal(sc.Bytes(), &m); err != nil {
			t.Fatal(err)
		}
		lines = append(lines, m)
	}
	if len(lines) != 2 || lines[0]["kind"] != "start" || lines[1]["kind"] != "cycle" {
		t.Fatalf("want start + cycle, got %v", lines)
	}
	if lines[0]["mode"] != "shadow" || lines[0]["host_class"] != "loopback" || lines[0]["read_only_required"] != false {
		t.Fatalf("start line %v", lines[0])
	}
	if strings.Contains(string(mustJSON(t, lines)), "PACIENTE-FICTICIO") {
		t.Fatal("payload leaked into the report")
	}
	items := lines[1]["items"].([]any)
	if len(items) != 3 {
		t.Fatalf("items %v", items)
	}
	byType := map[string]map[string]any{}
	for _, it := range items {
		m := it.(map[string]any)
		byType[m["type"].(string)+"/"+m["reason"].(string)] = m
	}
	a := byType["APPOINTMENT_CREATED/retry"]
	if a == nil || a["action"] != "recover" || a["known"] != true || a["attempts"].(float64) != 2 {
		t.Fatalf("APPOINTMENT_CREATED item %v (all %v)", a, byType)
	}
	if age := a["age_s"].(float64); age < 3*3600-60 || age > 3*3600+60 {
		t.Fatalf("age %v", age)
	}
	if d := a["destinations"].([]any); len(d) != 3 || d[0] != "notification" {
		t.Fatalf("destinations %v", d)
	}
	b := byType["APPOINTMENT_CONFIRMED/permanent_error"]
	if b == nil || b["action"] != "exhaust" || b["known"] != false {
		t.Fatalf("unknown type item %v (all %v)", b, byType)
	}
	if c := byType["PATIENT_UPDATED/attempts_exhausted"]; c == nil || c["action"] != "exhaust" {
		t.Fatalf("exhausted item missing: %v", byType)
	}
	after, _ := coll.Find(ctx, bson.D{})
	var b1 []bson.M
	_ = after.All(ctx, &b1)
	if string(mustJSON(t, b0)) != string(mustJSON(t, b1)) {
		t.Fatal("the shadow changed the database")
	}
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
