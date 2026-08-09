package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Update notice: a best-effort "you're behind" line, never a self-updater.
// Only tagged release builds check ("dev" builds skip it); one GET of the
// latest release tag, cached once per day beside the resume logs, time-boxed
// so it never blocks the run. Opt out with DISCORD_DELETE_NO_UPDATE_CHECK.

const (
	updateCheckEnv     = "DISCORD_DELETE_NO_UPDATE_CHECK"
	updateReleasesPage = "https://github.com/DatCodeMania/discord-delete/releases"
	updateCheckEvery   = 24 * time.Hour
	updateHTTPTimeout  = 3 * time.Second
	// A failed check retries inside the day instead of waiting for the next daily
	// window, but not so often that a blackholed network costs an
	// updateHTTPTimeout stall on every headless launch.
	updateRetryEvery = 6 * time.Hour
	// Ceiling on the release JSON. Real payloads run a few KB even with many
	// assets and long notes; this only stops an unbounded body.
	updateMaxBody = 1 << 20
)

// updateLatestURL is the endpoint that reports the newest release tag.
// Indirected for tests.
var updateLatestURL = "https://api.github.com/repos/DatCodeMania/discord-delete/releases/latest"

// startUpdateCheck runs the check in the background so startup never waits on
// the network. The returned channel yields the notice line, or "".
func startUpdateCheck() <-chan string {
	ch := make(chan string, 1)
	go func() { ch <- updateNotice(buildVersion, time.Now()) }()
	return ch
}

// updateNotice returns the notice line, or "" when up to date, disabled, or
// failed. Network is hit at most once per updateCheckEvery, or per
// updateRetryEvery once a check has failed, so an offline machine isn't probed
// every launch.
func updateNotice(current string, now time.Time) string {
	if current == "dev" || os.Getenv(updateCheckEnv) != "" {
		return ""
	}
	cache := filepath.Join(progressDir(), "update-check")
	checkedAt, latest := readUpdateCache(cache)
	// A tagless cache entry is a failed check, and it expires on the shorter
	// interval; caching a failure like a success would make one bad response
	// permanent.
	every := updateCheckEvery
	if latest == "" {
		every = updateRetryEvery
	}
	if now.Sub(checkedAt) >= every {
		ctx, cancel := context.WithTimeout(context.Background(), updateHTTPTimeout)
		tag, err := fetchLatestTag(ctx)
		cancel()
		latest = ""
		if err == nil {
			latest = tag
		}
		writeUpdateCache(cache, now, latest)
	}
	cur := strings.TrimPrefix(strings.TrimSpace(current), "v")
	latest = strings.TrimPrefix(strings.TrimSpace(latest), "v")
	if latest == "" || compareVersions(cur, latest) >= 0 {
		return ""
	}
	return fmt.Sprintf("Update available: %s (you have %s) · %s", latest, cur, updateReleasesPage)
}

// fetchLatestTag returns the latest release's tag_name from GitHub's
// GET /repos/{owner}/{repo}/releases/latest. Every failure, including an
// over-cap body, comes back as an error so the caller can retry it rather than
// record it as "no release".
func fetchLatestTag(ctx context.Context) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, updateLatestURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("User-Agent", "discord-delete/"+buildVersion)
	resp, err := (&http.Client{Timeout: updateHTTPTimeout}).Do(req)
	if err != nil {
		return "", err
	}
	// A LimitReader hits EOF at its bound exactly like a complete body, so read
	// one byte past the cap to tell truncation from a whole response.
	body, err := io.ReadAll(io.LimitReader(resp.Body, updateMaxBody+1))
	resp.Body.Close()
	if err != nil {
		return "", err
	}
	if len(body) > updateMaxBody {
		return "", fmt.Errorf("release JSON over %d bytes", updateMaxBody)
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("release lookup: %s", resp.Status)
	}
	var rel struct {
		TagName string `json:"tag_name"`
	}
	if err := json.Unmarshal(body, &rel); err != nil {
		return "", err
	}
	tag := strings.TrimSpace(rel.TagName)
	if tag == "" {
		return "", errors.New("release has no tag_name")
	}
	return tag, nil
}

// readUpdateCache parses "<RFC3339> <tag>"; a missing or malformed cache reads
// as never-checked.
func readUpdateCache(path string) (checkedAt time.Time, tag string) {
	data, err := os.ReadFile(path)
	if err != nil {
		return time.Time{}, ""
	}
	fields := strings.Fields(string(data))
	if len(fields) == 0 {
		return time.Time{}, ""
	}
	t, err := time.Parse(time.RFC3339, fields[0])
	if err != nil {
		return time.Time{}, ""
	}
	if len(fields) > 1 {
		tag = fields[1]
	}
	return t, tag
}

// writeUpdateCache is best-effort: an unwritable cache just means the check
// runs again next launch.
func writeUpdateCache(path string, checkedAt time.Time, tag string) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return
	}
	line := checkedAt.UTC().Format(time.RFC3339)
	if tag != "" {
		line += " " + tag
	}
	_ = os.WriteFile(path, []byte(line+"\n"), 0o600)
}

// compareVersions orders two dotted versions (-1, 0, 1), tolerating a leading
// "v" and a missing part as 0 (1.2 == 1.2.0). A pre-release sorts before its
// release (1.0.0-rc1 < 1.0.0); same-version pre-releases compare as strings.
func compareVersions(a, b string) int {
	av, apre := splitVersion(a)
	bv, bpre := splitVersion(b)
	for i := 0; i < len(av) || i < len(bv); i++ {
		an, bn := 0, 0
		if i < len(av) {
			an = av[i]
		}
		if i < len(bv) {
			bn = bv[i]
		}
		if an != bn {
			if an < bn {
				return -1
			}
			return 1
		}
	}
	switch {
	case apre == bpre:
		return 0
	case apre == "": // release > any of its pre-releases
		return 1
	case bpre == "":
		return -1
	case apre < bpre:
		return -1
	default:
		return 1
	}
}

// splitVersion turns "v1.2.3-rc1" into ([1 2 3], "rc1"); non-numeric parts read as 0.
func splitVersion(s string) (parts []int, pre string) {
	s = strings.TrimPrefix(strings.TrimSpace(s), "v")
	if i := strings.IndexByte(s, '-'); i >= 0 {
		s, pre = s[:i], s[i+1:]
	}
	for _, p := range strings.Split(s, ".") {
		n, err := strconv.Atoi(p)
		if err != nil {
			n = 0
		}
		parts = append(parts, n)
	}
	return parts, pre
}
