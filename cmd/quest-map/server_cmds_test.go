package main

import (
	"bytes"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/RoyalNoob/quest-map/internal/server"
)

func TestStopWithNoServer(t *testing.T) {
	root := t.TempDir()
	var out, errOut bytes.Buffer
	if code := run([]string{"stop", "--board", root}, &out, &errOut); code != 0 || !strings.Contains(out.String(), "no quest-map server is running") {
		t.Fatalf("stop = %d %q %q", code, out.String(), errOut.String())
	}
}

func TestStopAsksTheServerToShutDown(t *testing.T) {
	root := t.TempDir()
	s, err := server.Start(root, server.Options{Port: -1})
	if err != nil {
		t.Fatal(err)
	}
	pid := s.Info().PID
	// What `quest-map serve` does when a stop is requested.
	closed := make(chan error, 1)
	go func() { <-s.StopRequested(); closed <- s.Close() }()

	var out, errOut bytes.Buffer
	if code := run([]string{"stop", "--board", root}, &out, &errOut); code != 0 {
		t.Fatalf("stop = %d %q", code, errOut.String())
	}
	select {
	case err := <-closed:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the server did not close")
	}
	if !strings.Contains(out.String(), fmt.Sprintf("stopped the quest-map server for %s (pid %d)", root, pid)) || !lockFree(root) || server.Running(root) != nil {
		t.Errorf("after stop: %q, lock free %v", out.String(), lockFree(root))
	}
	if _, err := server.ReadInfo(root); err == nil {
		t.Error("server.json is still there")
	}
}

func TestShutdownNeedsTheToken(t *testing.T) {
	root := t.TempDir()
	s, err := server.Start(root, server.Options{Port: -1})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	info := s.Info()
	bad := info
	bad.Token = "wrong"
	if err := server.CallJSONTimeout(&bad, http.MethodPost, "/api/shutdown", nil, nil, 2*time.Second); err == nil {
		t.Fatal("shutdown with a wrong token worked")
	}
	select {
	case <-s.StopRequested():
		t.Fatal("stop requested without the token")
	default:
	}
}
