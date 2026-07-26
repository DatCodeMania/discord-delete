package main

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// Crash-safe resume log. The data package is never modified, so to avoid
// re-issuing DELETEs a previous run already settled (each costs a request just
// to get a 404), every message ID needing no further request is appended to a
// per-package log on disk: the confirmed-gone ones, plus system messages, which
// answer 403 forever. A lost-access 403 is not logged, since rejoining makes it
// deletable again. On the next launch the log is loaded and those IDs are
// filtered out before anything is queued. The log holds only message IDs, never
// the token or message content.
//
// It's append-and-flush per delete, so a crash or kill loses at most the
// message in flight: the OS keeps already-written bytes even if the process
// dies.

// progressDir is where per-package logs live. Overridable for tests.
func progressDir() string {
	if d := strings.TrimSpace(os.Getenv("DISCORD_DELETE_STATE_DIR")); d != "" {
		return d
	}
	if d, err := os.UserCacheDir(); err == nil && d != "" {
		return filepath.Join(d, "discord-delete", "progress")
	}
	return filepath.Join(os.TempDir(), "discord-delete", "progress")
}

// progressKey identifies the log for a package. Keyed by the owning account
// when known, so a re-requested package for the same account resumes
// (message IDs are globally unique, so sharing is correct); otherwise keyed
// by a hash of the path.
func progressKey(owner PackageOwner, pkgPath string) string {
	if owner.ID != "" {
		return "user-" + owner.ID
	}
	abs, err := filepath.Abs(pkgPath)
	if err != nil {
		abs = pkgPath
	}
	sum := sha256.Sum256([]byte(abs))
	return "pkg-" + hex.EncodeToString(sum[:8])
}

func progressPath(owner PackageOwner, pkgPath string) string {
	return filepath.Join(progressDir(), progressKey(owner, pkgPath)+".deleted.log")
}

// loadProgressSet reads the set of already-deleted message IDs. A missing or
// unreadable log simply yields an empty set (nothing to resume).
func loadProgressSet(path string) map[string]bool {
	set := map[string]bool{}
	f, err := os.Open(path)
	if err != nil {
		return set
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for sc.Scan() {
		if id := strings.TrimSpace(sc.Text()); id != "" {
			set[id] = true
		}
	}
	return set
}

// progressLog appends confirmed-gone message IDs, flushing each so a crash can't
// lose more than the in-flight delete. Safe for concurrent workers.
type progressLog struct {
	mu  sync.Mutex
	f   *os.File
	w   *bufio.Writer
	err error // first failed append; bufio's error is sticky, so the log is dead after
}

func openProgressLog(path string) (*progressLog, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, err
	}
	return &progressLog{f: f, w: bufio.NewWriter(f)}, nil
}

func (p *progressLog) record(msgID string) {
	if p == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.err != nil {
		return
	}
	p.w.WriteString(msgID)
	p.w.WriteByte('\n')
	// Flush now so a process crash keeps this record. Flush also returns the
	// writer's sticky error, covering the two unchecked writes above.
	if err := p.w.Flush(); err != nil {
		p.err = err
	}
}

// writeErr reports the first failed append or flush; every record after it was
// dropped, so those deletions repeat on the next run.
func (p *progressLog) writeErr() error {
	if p == nil {
		return nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.err
}

func (p *progressLog) close() {
	if p == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if err := p.w.Flush(); err != nil && p.err == nil {
		p.err = err
	}
	if err := p.f.Close(); err != nil && p.err == nil {
		p.err = err
	}
}

// probeProgressLog checks that the log can be created and opened for append,
// so an execute run can refuse up front instead of deleting without a resume
// record.
func probeProgressLog(path string) error {
	pl, err := openProgressLog(path)
	if err != nil {
		return err
	}
	pl.close()
	return nil
}

func countInSet(raws []RawChannel, set map[string]bool) int {
	if len(set) == 0 {
		return 0
	}
	n := 0
	for _, rc := range raws {
		for _, m := range rc.Messages {
			if set[m.ID] {
				n++
			}
		}
	}
	return n
}

// reactionProgressPath is the resume log for reaction removals, kept separate
// from the message log so their keys never collide.
func reactionProgressPath(owner PackageOwner, pkgPath string) string {
	return filepath.Join(progressDir(), progressKey(owner, pkgPath)+".reactions.log")
}
