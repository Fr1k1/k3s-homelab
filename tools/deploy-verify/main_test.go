package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestFetchGitSHA(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(healthResponse{Status: "ok", Service: "vjenchanje-api", GitSHA: "abc1234"})
	}))
	defer server.Close()

	sha, err := fetchGitSHA(server.Client(), server.URL)
	if err != nil {
		t.Fatalf("fetchGitSHA returned error: %v", err)
	}
	if sha != "abc1234" {
		t.Errorf("got sha %q, want %q", sha, "abc1234")
	}
}

func TestFetchGitSHA_NonOKStatus(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()

	_, err := fetchGitSHA(server.Client(), server.URL)
	if err == nil {
		t.Fatal("expected error for a 503 response, got nil")
	}
}

func TestPollUntilMatch_SucceedsOnceSHAMatches(t *testing.T) {
	var calls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		sha := "old-sha"
		if calls >= 3 {
			sha = "new-sha"
		}
		json.NewEncoder(w).Encode(healthResponse{Status: "ok", GitSHA: sha})
	}))
	defer server.Close()

	err := pollUntilMatch(server.Client(), server.URL, "new-sha", 2*time.Second, 20*time.Millisecond)
	if err != nil {
		t.Fatalf("pollUntilMatch returned error: %v", err)
	}
	if calls < 3 {
		t.Errorf("expected at least 3 calls before match, got %d", calls)
	}
}

func TestPollUntilMatch_TimesOut(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(healthResponse{Status: "ok", GitSHA: "never-matches"})
	}))
	defer server.Close()

	err := pollUntilMatch(server.Client(), server.URL, "new-sha", 100*time.Millisecond, 20*time.Millisecond)
	if err == nil {
		t.Fatal("expected a timeout error, got nil")
	}
}
