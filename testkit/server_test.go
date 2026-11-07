package testkit

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestNewServer_ServesWithoutATestingTB(t *testing.T) {
	up, stop := NewServer(Options{})
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, up.URL+PathFor("openai/chat"), strings.NewReader(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set(FixtureHeader, "openai/chat")
	resp, err := up.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	want, _ := Fixture("openai/chat")
	if resp.StatusCode != 200 || string(got) != string(want) || len(up.Requests()) != 1 {
		t.Fatalf("status %d, %d bytes, %d requests", resp.StatusCode, len(got), len(up.Requests()))
	}
	stop()
	if _, err := up.Client().Do(req.Clone(context.Background())); err == nil {
		t.Fatal("the server still answers after stop")
	}
}
