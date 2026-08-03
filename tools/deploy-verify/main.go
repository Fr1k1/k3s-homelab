package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"
)

type healthResponse struct {
	Status  string `json:"status"`
	Service string `json:"service"`
	GitSHA  string `json:"git_sha"`
}

func fetchGitSHA(client *http.Client, url string) (string, error) {
	resp, err := client.Get(url)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("unexpected status code %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}

	var health healthResponse
	if err := json.Unmarshal(body, &health); err != nil {
		return "", fmt.Errorf("decoding response: %w", err)
	}

	return health.GitSHA, nil
}

func pollUntilMatch(client *http.Client, url, wantSHA string, timeout, interval time.Duration) error {
	deadline := time.Now().Add(timeout)
	var lastSHA string
	var lastErr error

	for {
		sha, err := fetchGitSHA(client, url)
		if err == nil {
			lastErr = nil
			lastSHA = sha
			if sha == wantSHA {
				return nil
			}
		} else {
			lastErr = err
		}

		if time.Now().After(deadline) {
			if lastErr != nil {
				return fmt.Errorf("timed out after %s waiting for git_sha=%s: last error: %w", timeout, wantSHA, lastErr)
			}
			return fmt.Errorf("timed out after %s waiting for git_sha=%s: last saw %q", timeout, wantSHA, lastSHA)
		}

		time.Sleep(interval)
	}
}

func main() {
	url := flag.String("url", "", "URL of the API's health endpoint to poll")
	wantSHA := flag.String("want-sha", "", "the git SHA expected to be live")
	timeout := flag.Duration("timeout", 10*time.Minute, "how long to poll before giving up")
	interval := flag.Duration("interval", 10*time.Second, "how long to wait between polls")
	flag.Parse()

	if *url == "" || *wantSHA == "" {
		fmt.Fprintln(os.Stderr, "usage: deploy-verify -url <url> -want-sha <sha> [-timeout 10m] [-interval 10s]")
		os.Exit(2)
	}

	client := &http.Client{Timeout: 10 * time.Second}

	fmt.Printf("waiting for %s to report git_sha=%s (timeout %s)\n", *url, *wantSHA, *timeout)

	if err := pollUntilMatch(client, *url, *wantSHA, *timeout, *interval); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	fmt.Println("deploy verified")
}
