package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestResponseHistory(t *testing.T) {
	dir := t.TempDir()
	r := newRequest("t", "GET", "example.org")
	ws := &workspace{Collections: []*collection{{Requests: []*request{r}}}}
	// A response kept as the only one, before the history.
	data := []byte(`{"Code":201,"At":"2026-01-02T03:04:05.000000006Z"}`)
	os.WriteFile(filepath.Join(dir, r.ID+".json"), data, 0o600)
	os.WriteFile(filepath.Join(dir, "gone.json"), data, 0o600)
	pruneResponses(dir, ws)
	hist := filepath.Join(dir, r.ID)
	if got := entries(hist); len(got) != 1 || got[0].Code != 201 || got[0].At.Nanosecond() != 6 {
		t.Fatalf("migrated %v", got)
	}
	if _, err := os.Stat(filepath.Join(dir, "gone.json")); err == nil {
		t.Error("kept the response of a request deleted")
	}

	start := time.Now()
	for i := range maxHistory + 2 {
		saveResponse(hist, &response{Code: 200, At: start.Add(time.Duration(i) * time.Second)})
	}
	got := entries(hist)
	if len(got) != maxHistory || !got[0].At.Equal(start.Add((maxHistory+1)*time.Second)) {
		t.Errorf("history %d, newest %v", len(got), got[0].At)
	}
	if res := readResponse(filepath.Join(hist, got[0].file())); res == nil || !got[0].is(res) {
		t.Errorf("read %v", res)
	}
}
