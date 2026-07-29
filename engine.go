package main

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
)

// apiBaseOverride points the API at a test server. Engines snapshot it when
// constructed, so a later change never reaches a running engine.
var apiBaseOverride string

func apiBaseURL() string {
	if apiBaseOverride != "" {
		return apiBaseOverride
	}
	return "https://discord.com/api/v9"
}

// A plain browser-like User-Agent: standard HTTP hygiene so the user endpoints
// accept the request, not fingerprint spoofing. We do not fabricate
// X-Super-Properties, rotate IPs, or otherwise try to defeat abuse detection.
const userAgent = "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 " +
	"(KHTML, like Gecko) Chrome/126.0.0.0 Safari/537.36"

// EngineConfig holds runtime knobs.
type EngineConfig struct {
	Token       string
	Workers     int
	DeleteDelay time.Duration // base per-channel spacing (human-paced)
	Jitter      float64       // +/- fraction on DeleteDelay
	DryRun      bool

	// GlobalMinInterval is a hard floor on spacing between ANY two requests,
	// account-wide. It caps total throughput well under Discord's 50 req/s so
	// we never approach the global limit even with many workers.
	GlobalMinInterval time.Duration

	// OnDeleted, if set, is called with the resume key of every item needing no
	// further request: confirmed gone (deleted or 404), or undeletable forever
	// (system-message 403). The key is persisted to the resume log and skipped
	// on a later run. Not called for a retryable 403 (lost access), or skip/fail.
	OnDeleted func(key string)

	// CF, if set, is the caller's Cloudflare invalid-response budget. Discord
	// counts those responses per IP over a rolling 10 minutes, so every phase
	// and run in the process shares one. A nil CF gets a private budget.
	CF *cfBudget
}

// WorkerStatus is a snapshot of one worker for the TUI.
type WorkerStatus struct {
	Active  bool
	Channel string
	Done    int
	Total   int
}

// Stats is the shared, concurrency-safe state the TUI reads each frame.
type Stats struct {
	total   int64
	deleted int64
	skipped int64
	failed  int64

	startNano     int64
	finished      atomic.Bool // run has returned (for any reason: done, stopped, aborted)
	completed     atomic.Bool // every job was processed (not stopped or aborted early)
	aborted       atomic.Bool
	activeLimit   atomic.Int64 // current max concurrent workers (< len(workers) once collapsed)
	invalidWindow atomic.Int64 // invalid (401/403/429) responses in the last 10 min (Cloudflare budget)

	mu        sync.Mutex
	workers   []WorkerStatus
	errRing   []string
	statusLn  string
	forbidden map[string]*ForbiddenStat // channelID -> 403 tally + Discord's reason
}

// ForbiddenStat is the per-channel record of 403 (undeletable) responses: how
// many, and the reason Discord gave (its error "message", e.g. "Missing
// Permissions" or "Cannot execute action on a system message").
type ForbiddenStat struct {
	Count  int
	Reason string
}

func NewStats(total, workers int) *Stats {
	return &Stats{
		total:     int64(total),
		startNano: time.Now().UnixNano(),
		workers:   make([]WorkerStatus, workers),
	}
}

func (s *Stats) addDeleted() { atomic.AddInt64(&s.deleted, 1) }
func (s *Stats) addSkipped() { atomic.AddInt64(&s.skipped, 1) }
func (s *Stats) addFailed()  { atomic.AddInt64(&s.failed, 1) }

func (s *Stats) setWorker(i int, w WorkerStatus) {
	s.mu.Lock()
	if i >= 0 && i < len(s.workers) {
		s.workers[i] = w
	}
	s.mu.Unlock()
}

// logForbidden records a 403 (undeletable) response for a channel, keeping the
// first reason Discord gave, so the end-of-run report can roll these up by
// server and explain why.
func (s *Stats) logForbidden(channelID, reason string) {
	s.mu.Lock()
	if s.forbidden == nil {
		s.forbidden = map[string]*ForbiddenStat{}
	}
	fs := s.forbidden[channelID]
	if fs == nil {
		fs = &ForbiddenStat{}
		s.forbidden[channelID] = fs
	}
	fs.Count++
	if fs.Reason == "" {
		fs.Reason = reason
	}
	s.mu.Unlock()
}

func (s *Stats) logErr(format string, a ...any) {
	line := fmt.Sprintf(format, a...)
	s.mu.Lock()
	s.errRing = append(s.errRing, line)
	if len(s.errRing) > 200 {
		s.errRing = s.errRing[len(s.errRing)-200:]
	}
	s.mu.Unlock()
}

func (s *Stats) setStatus(line string) {
	s.mu.Lock()
	s.statusLn = line
	s.mu.Unlock()
}

func (s *Stats) setActiveLimit(n int)   { s.activeLimit.Store(int64(n)) }
func (s *Stats) setInvalidWindow(n int) { s.invalidWindow.Store(int64(n)) }

// Snapshot is an immutable view for rendering.
type Snapshot struct {
	Total, Deleted, Skipped, Failed int64
	Processed                       int64
	Elapsed                         time.Duration
	Rate                            float64 // deletions/sec
	ETA                             time.Duration
	Workers                         []WorkerStatus
	Errors                          []string
	Status                          string
	Finished, Completed, Aborted    bool
	ActiveLimit                     int                      // current concurrency cap; < len(Workers) means collapsed
	InvalidWindow                   int                      // invalid responses in the last 10 min (Cloudflare budget)
	Forbidden                       map[string]ForbiddenStat // channelID -> undeletable (403) tally + reason
}

func (s *Stats) Snapshot() Snapshot {
	deleted := atomic.LoadInt64(&s.deleted)
	skipped := atomic.LoadInt64(&s.skipped)
	failed := atomic.LoadInt64(&s.failed)
	processed := deleted + skipped + failed
	elapsed := time.Duration(time.Now().UnixNano() - s.startNano)
	rate := 0.0
	if elapsed > 0 {
		rate = float64(deleted) / elapsed.Seconds()
	}
	var eta time.Duration
	remaining := s.total - processed
	if rate > 0.01 && remaining > 0 {
		eta = time.Duration(float64(remaining)/rate) * time.Second
	}
	s.mu.Lock()
	workers := make([]WorkerStatus, len(s.workers))
	copy(workers, s.workers)
	errs := make([]string, len(s.errRing))
	copy(errs, s.errRing)
	status := s.statusLn
	var forbidden map[string]ForbiddenStat
	if len(s.forbidden) > 0 {
		forbidden = make(map[string]ForbiddenStat, len(s.forbidden))
		for k, v := range s.forbidden {
			forbidden[k] = *v
		}
	}
	s.mu.Unlock()
	return Snapshot{
		Total: s.total, Deleted: deleted, Skipped: skipped, Failed: failed,
		Processed: processed, Elapsed: elapsed, Rate: rate, ETA: eta,
		Workers: workers, Errors: errs, Status: status,
		Finished: s.finished.Load(), Completed: s.completed.Load(), Aborted: s.aborted.Load(),
		Forbidden:     forbidden,
		ActiveLimit:   int(s.activeLimit.Load()),
		InvalidWindow: int(s.invalidWindow.Load()),
	}
}

// limiter enforces the account-wide request floor and honors global 429 pauses.
type limiter struct {
	mu          sync.Mutex
	next        time.Time
	minInterval time.Duration

	pauseUntilMs  atomic.Int64
	invalidStreak atomic.Int64
	paused        atomic.Bool // user-controlled indefinite pause (hotkey)
}

// gate blocks until this request is allowed to fire (respecting both the global
// pause window and the account-wide spacing). Returns false if ctx is done.
func (l *limiter) gate(ctx context.Context) bool {
	for {
		// Respect a user-controlled pause (hotkey) first, holding here until resumed.
		for l.paused.Load() {
			if !sleepCtx(ctx, 150*time.Millisecond) {
				return false
			}
		}
		// Respect a global-429 pause next.
		if until := l.pauseUntilMs.Load(); until > 0 {
			if d := time.Until(time.UnixMilli(until)); d > 0 {
				if !sleepCtx(ctx, d) {
					return false
				}
			}
		}
		// Account-wide spacing.
		l.mu.Lock()
		now := time.Now()
		wait := time.Duration(0)
		if now.Before(l.next) {
			wait = l.next.Sub(now)
		}
		l.next = now.Add(wait).Add(l.minInterval)
		l.mu.Unlock()
		if wait > 0 {
			if !sleepCtx(ctx, wait) {
				return false
			}
		}
		// A pause that arrived while this worker slept on its spacing slot must
		// still be honored: firing now would land the request inside the pause
		// window (defeating pauseGlobal's all-workers promise), so loop back and
		// wait it out. The extra slot taken on the retry only widens spacing.
		if l.paused.Load() {
			continue
		}
		if until := l.pauseUntilMs.Load(); until > 0 && time.Now().UnixMilli() < until {
			continue
		}
		return true
	}
}

// pauseGlobal parks every worker until now+d. Concurrent callers take the
// max, so a shorter pause never shrinks a longer one that's already in effect.
func (l *limiter) pauseGlobal(d time.Duration) {
	if d <= 0 {
		return
	}
	until := time.Now().Add(d).UnixMilli()
	for {
		cur := l.pauseUntilMs.Load()
		if until <= cur {
			return
		}
		if l.pauseUntilMs.CompareAndSwap(cur, until) {
			return
		}
	}
}

// invalidAbortThreshold is how many consecutive 401 Unauthorized responses trip
// the safety abort. A wall of 401s means the token is dead/expired (or access
// was lost), which won't recover by waiting, so we stop, well before Discord's
// ~10k-invalid/10-min Cloudflare budget. Rate limits (429) are NOT counted:
// they're the normal "slow down" signal and are handled by backing off, never
// by aborting. Expected 403 skips don't feed it either.
const invalidAbortThreshold = 20

// Engine deletes messages for a set of channel jobs.
type Engine struct {
	cfg    EngineConfig
	stats  *Stats
	client *http.Client
	lim    *limiter
	conc   *concurrencyController
	cf     *cfBudget
	rng    *rand.Rand
	rngMu  sync.Mutex

	// apiBase is resolved once at construction and never written again, so the
	// workers read an immutable field instead of package state.
	apiBase string

	// baseDelayMs is the per-channel spacing floor in milliseconds, held as an
	// atomic so a hotkey can nudge pacing live mid-run.
	baseDelayMs atomic.Int64

	// abort cancels the engine's own derived context. bumpInvalid calls it so
	// the safety abort stops every worker immediately, rather than only setting
	// a flag and hoping the UI notices and cancels.
	abort context.CancelFunc
}

func NewEngine(cfg EngineConfig, stats *Stats) *Engine {
	cf := cfg.CF
	if cf == nil {
		cf = newCFBudget()
	}
	e := &Engine{
		cfg:     cfg,
		stats:   stats,
		client:  &http.Client{Timeout: 30 * time.Second},
		lim:     &limiter{minInterval: cfg.GlobalMinInterval},
		conc:    newConcurrencyController(cfg.Workers, stats),
		cf:      cf,
		rng:     rand.New(rand.NewSource(time.Now().UnixNano())),
		apiBase: apiBaseURL(),
	}
	// Without this an inherited window reads as 0 until this phase records an
	// invalid response of its own.
	stats.setInvalidWindow(cf.count())
	e.baseDelayMs.Store(cfg.DeleteDelay.Milliseconds())
	return e
}

// togglePaused flips the user pause and returns the new state. Only the UI
// goroutine calls it, so the load-then-store is safe.
func (e *Engine) togglePaused() bool {
	np := !e.lim.paused.Load()
	e.lim.paused.Store(np)
	return np
}

func (e *Engine) isPaused() bool { return e.lim.paused.Load() }

// setPaused forces the pause state. Used by remote control; safe from any
// goroutine since it's a single atomic store.
func (e *Engine) setPaused(p bool) { e.lim.paused.Store(p) }

func (e *Engine) baseDelay() time.Duration {
	return time.Duration(e.baseDelayMs.Load()) * time.Millisecond
}

// nudgeDelay adjusts the spacing floor by delta (clamped to [0, 30s]) and
// returns the new value. Takes effect on every worker's next inter-delete wait.
func (e *Engine) nudgeDelay(delta time.Duration) time.Duration {
	const maxMs = int64(30 * 1000)
	for {
		cur := e.baseDelayMs.Load()
		n := cur + delta.Milliseconds()
		if n < 0 {
			n = 0
		}
		if n > maxMs {
			n = maxMs
		}
		if e.baseDelayMs.CompareAndSwap(cur, n) {
			return time.Duration(n) * time.Millisecond
		}
	}
}

// Run processes all jobs and returns when finished or ctx is cancelled.
func (e *Engine) Run(ctx context.Context, jobs []ChannelJob) {
	defer e.stats.finished.Store(true)

	// Derive a cancelable context the engine owns, so the safety abort (see
	// bumpInvalid) can stop all workers immediately without waiting on the UI.
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	e.abort = cancel

	// sync.Cond isn't context-aware, so wake any workers parked on the
	// concurrency gate when the run ends, letting them observe ctx and exit.
	go func() {
		<-ctx.Done()
		e.conc.wakeAll()
	}()

	jobCh := make(chan ChannelJob)
	var wg sync.WaitGroup
	for i := 0; i < e.cfg.Workers; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for job := range jobCh {
				if ctx.Err() != nil {
					return
				}
				e.runChannel(ctx, id, job)
				e.stats.setWorker(id, WorkerStatus{Active: false})
			}
		}(i)
	}

	go func() {
		defer close(jobCh)
		for _, j := range jobs {
			select {
			case <-ctx.Done():
				return
			case jobCh <- j:
			}
		}
	}()

	wg.Wait()
	// Reaching here with the context intact means every job was processed. A user
	// stop or the 401 safety-abort cancels ctx first, so those never count as a
	// completed run. Checked before the deferred cancel() above fires.
	if ctx.Err() == nil {
		e.stats.completed.Store(true)
	}
}

func (e *Engine) runChannel(ctx context.Context, workerID int, job ChannelJob) {
	items := job.Items
	if items == nil {
		items = make([]deleteItem, len(job.MsgIDs))
		for i, id := range job.MsgIDs {
			items[i] = deleteItem{MessageID: id, Key: id}
		}
	}
	total := len(items)
	// Adaptive per-channel spacing (AIMD). The configured DeleteDelay is the
	// FLOOR (fastest we'll ever go); `cur` starts there and backs off above it
	// when Discord throttles this channel's bucket, then recovers back down as
	// deletes succeed. Buckets are per-channel, so this state is per-channel and
	// resets each job. A strict channel slows without dragging down the rest.
	cur := e.baseDelay()
	for i, item := range items {
		if ctx.Err() != nil {
			return
		}
		// Read the floor each message so a live pacing nudge takes effect now.
		base := e.baseDelay()
		// Take an account-wide concurrency slot for the whole delete (incl. its
		// retries/backoff). Once throttling collapses the limit to 1, the other
		// workers park here, shown idle, instead of piling redundant 429s onto
		// the same shared bucket. Held through deleteOne so exactly one request is
		// in flight while collapsed.
		if !e.conc.acquire(ctx, func() {
			e.stats.setWorker(workerID, WorkerStatus{Active: false})
		}) {
			return
		}
		e.stats.setWorker(workerID, WorkerStatus{
			Active: true, Channel: job.Label, Done: i, Total: total,
		})
		throttled := e.deleteOne(ctx, job.ChannelID, item, job.Label)
		e.conc.release()
		cur = adaptSpacing(cur, base, throttled)

		// Human-paced spacing between deletes in the SAME channel. This is the
		// primary "don't look like a bot" mechanism; concurrency across
		// different channels is where the speed comes from. Skipped in dry-run
		// so previews are instant.
		if !e.cfg.DryRun && i < total-1 {
			if !sleepCtx(ctx, e.jitter(cur)) {
				return
			}
		}
	}
	e.stats.setWorker(workerID, WorkerStatus{
		Active: true, Channel: job.Label, Done: total, Total: total,
	})
}

// AIMD tuning. Kept base-relative (scale-free) so it adapts proportionally to
// whatever floor the user chose, and so a delay of 0 (pacing off) stays off.
const (
	adaptiveBackoff  = 1.6              // multiplicative increase per throttled delete
	adaptiveCeilMult = 8.0              // ceiling = floor * this …
	adaptiveCeilMax  = 30 * time.Second // … but never longer than this
	adaptiveRecover  = 0.2              // additive recovery per clean delete, as a fraction of the floor
)

// adaptSpacing returns the next inter-delete spacing given the current spacing,
// the floor (configured delay), and whether the last delete saw a 429. Throttle
// => multiplicative back-off up to a ceiling; success => additive recovery down
// to the floor. A floor of 0 disables adaptation entirely.
func adaptSpacing(cur, floor time.Duration, throttled bool) time.Duration {
	if floor <= 0 {
		return 0
	}
	if throttled {
		next := time.Duration(float64(cur) * adaptiveBackoff)
		ceiling := time.Duration(float64(floor) * adaptiveCeilMult)
		if ceiling > adaptiveCeilMax {
			ceiling = adaptiveCeilMax
		}
		if next > ceiling {
			next = ceiling
		}
		return next
	}
	next := cur - time.Duration(float64(floor)*adaptiveRecover)
	if next < floor {
		next = floor
	}
	return next
}

// markDone logs an item needing no further request (confirmed gone, or
// undeletable forever) by its resume key.
func (e *Engine) markDone(key string) {
	if e.cfg.OnDeleted != nil {
		e.cfg.OnDeleted(key)
	}
}

// jitter applies the configured +/- jitter fraction to a spacing duration.
func (e *Engine) jitter(d time.Duration) time.Duration {
	if e.cfg.Jitter <= 0 {
		return d
	}
	e.rngMu.Lock()
	f := (e.rng.Float64()*2 - 1) * e.cfg.Jitter
	e.rngMu.Unlock()
	j := time.Duration(float64(d) * (1 + f))
	if j < 0 {
		return 0
	}
	return j
}

// deleteOne deletes one message, retrying per its budgets. It reports whether
// it saw any rate-limiting (a 429) so the caller can widen this channel's
// spacing before the next delete.
func (e *Engine) deleteOne(ctx context.Context, channelID string, item deleteItem, label string) (throttled bool) {
	if e.cfg.DryRun {
		e.stats.addDeleted()
		return false
	}

	// Transient errors (network, 5xx) share one bounded budget; being
	// rate-limited (429) is not an error and gets its own, more generous one so
	// a hard-throttled-but-deletable message is never marked failed.
	const maxErrRetries = 8
	const maxRateRetries = 12
	backoff := time.Second
	errRetries := 0
	rateRetries := 0

	for {
		if ctx.Err() != nil {
			return
		}
		// Hold if we're near Discord's Cloudflare invalid-response cap.
		if !e.cf.waitIfExhausted(ctx, e.stats) {
			return
		}
		if !e.lim.gate(ctx) {
			return
		}

		var url string
		if item.Emoji == "" {
			url = fmt.Sprintf("%s/channels/%s/messages/%s", e.apiBase, channelID, item.MessageID)
		} else {
			url = fmt.Sprintf("%s/channels/%s/messages/%s/reactions/%s/@me", e.apiBase, channelID, item.MessageID, item.Emoji)
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodDelete, url, nil)
		if err != nil {
			// A malformed id from the package can't be turned into a request.
			// Skip it rather than crashing the worker on a nil request.
			e.stats.logErr("skip %s: bad request: %v", item.MessageID, err)
			e.stats.addSkipped()
			return
		}
		req.Header.Set("Authorization", e.cfg.Token)
		req.Header.Set("User-Agent", userAgent)
		req.Header.Set("Accept", "application/json")

		resp, err := e.client.Do(req)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			errRetries++
			if errRetries >= maxErrRetries {
				e.stats.logErr("gave up on %s after %d network errors: %v", item.MessageID, errRetries, err)
				e.stats.addFailed()
				return
			}
			e.stats.logErr("net error on %s: %v (retry in %s)", item.MessageID, err, backoff)
			if !sleepCtx(ctx, backoff) {
				return
			}
			backoff = min(backoff*2, 30*time.Second)
			continue
		}

		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
		resp.Body.Close()

		// Count invalid responses toward the Cloudflare 10k/10-min budget. A 404
		// ("already gone") is a success, and shared-scope 429s are exempt per
		// Discord's docs, so only 401/403 and non-shared 429s count.
		if sc := resp.StatusCode; sc == 401 || sc == 403 ||
			(sc == 429 && resp.Header.Get("X-RateLimit-Scope") != "shared") {
			e.cf.record(time.Now(), e.stats)
		}

		switch {
		case resp.StatusCode == 204 || resp.StatusCode == 200:
			e.lim.invalidStreak.Store(0)
			e.observeBucket(ctx, resp.Header)
			e.stats.addDeleted()
			e.markDone(item.Key)
			return

		case resp.StatusCode == 404:
			// Already gone: goal achieved.
			e.lim.invalidStreak.Store(0)
			e.stats.addDeleted()
			e.markDone(item.Key)
			return

		case resp.StatusCode == 429:
			throttled = true
			retry := parseRetryAfter(body, resp.Header)
			scope := resp.Header.Get("X-RateLimit-Scope")
			// A 429 is a normal "slow down" signal, NEVER an invalid/abort
			// condition, so it does not touch the invalid-request budget.
			//
			// Pause ALL workers for retry_after, not just this one. Discord's
			// delete limit for old messages is effectively per-account, so one
			// worker's cooldown applies to everyone; otherwise the other workers
			// keep hammering the same limit and the whole run just churns 429s.
			e.lim.pauseGlobal(retry)
			// An account-wide scope (shared/global) means the limit is one bucket
			// for the whole account: this is what deleting old messages hits, and
			// extra workers can't beat it, only add redundant 429s. Signal the
			// controller so it can collapse to a single worker. A per-channel
			// ("user"/bucket) 429 is left alone since parallelism across channels
			// helps there.
			accountWide := isGlobal429(body, resp.Header) || scope == "shared"
			// Collapse to one worker only for messages (the account-wide old-message
			// bucket). Reaction buckets are per-channel, so collapsing would
			// needlessly serialize deletion across channels.
			if accountWide && item.Emoji == "" {
				e.conc.sharedThrottle()
			}
			if isGlobal429(body, resp.Header) {
				e.stats.setStatus(fmt.Sprintf("global rate limit; all workers paused %.1fs", retry.Seconds()))
			} else {
				e.stats.setStatus(fmt.Sprintf("rate limited (%s) on %s; easing off all workers %.1fs", cmp.Or(scope, "bucket"), label, retry.Seconds()))
			}
			rateRetries++
			if rateRetries >= maxRateRetries {
				// Still throttled after honoring every retry_after. Defer it to a
				// later run (resumable) rather than marking a deletable message
				// failed. This is expected under heavy throttling, not an error,
				// so it's a status line, not a red error entry.
				e.stats.setStatus(fmt.Sprintf("deferring %s to a later run; still rate-limited after %d tries", label, rateRetries))
				e.stats.addSkipped()
				return
			}
			if !sleepCtx(ctx, retry+50*time.Millisecond) {
				return
			}
			continue

		case resp.StatusCode == 403:
			// Undeletable (system message, or access lost). Skip, don't retry.
			// This is expected and benign, so it does NOT feed the abort streak.
			// Record it per-channel with Discord's own reason so the report can
			// name the servers involved and explain why.
			e.stats.logForbidden(channelID, parseAPIErrorMessage(body))
			e.stats.addSkipped()
			// A system message is undeletable forever, so later runs would spend
			// a request on the same 403. Lost-access 403s stay unlogged:
			// rejoining makes those deletable again.
			if parseAPIErrorCode(body) == errSystemMessage {
				e.markDone(item.Key)
			}
			return

		case resp.StatusCode == 401:
			e.bumpInvalid()
			e.stats.logErr("401 Unauthorized on %s; token invalid or expired?", item.MessageID)
			e.stats.addFailed()
			return

		case resp.StatusCode >= 500:
			errRetries++
			if errRetries >= maxErrRetries {
				e.stats.logErr("gave up on %s after %d server errors (last %d)", item.MessageID, errRetries, resp.StatusCode)
				e.stats.addFailed()
				return
			}
			e.stats.setStatus(fmt.Sprintf("server %d on %s; retry %s", resp.StatusCode, label, backoff))
			if !sleepCtx(ctx, backoff) {
				return
			}
			backoff = min(backoff*2, 30*time.Second)
			continue

		default:
			e.stats.logErr("delete %s: HTTP %d %s", item.MessageID, resp.StatusCode, snippet(body))
			e.stats.addFailed()
			return
		}
	}
}

// observeBucket proactively sleeps when a per-route bucket is exhausted, so the
// next request in that bucket doesn't come back as a 429.
func (e *Engine) observeBucket(ctx context.Context, h http.Header) {
	if h.Get("X-RateLimit-Remaining") != "0" {
		return
	}
	if v := h.Get("X-RateLimit-Reset-After"); v != "" {
		if secs, err := strconv.ParseFloat(v, 64); err == nil && secs > 0 {
			sleepCtx(ctx, time.Duration(secs*float64(time.Second))+50*time.Millisecond)
		}
	}
}

// bumpInvalid counts consecutive 401 Unauthorized responses and aborts the run
// if a wall of them accrues: that means the token is dead/expired (or access
// was lost), which won't recover by waiting, so we stop to protect the account
// (well short of Discord's ~10k-invalid/10-min Cloudflare budget). Only 401
// calls this; rate limits and 403 skips do not.
func (e *Engine) bumpInvalid() {
	if e.lim.invalidStreak.Add(1) >= invalidAbortThreshold {
		e.stats.aborted.Store(true)
		e.stats.setStatus("ABORTED: repeated 401 Unauthorized. The token looks invalid or expired, so stopping to protect the account.")
		if e.abort != nil {
			e.abort() // stop every worker immediately, don't wait on the UI
		}
	}
}

func sleepCtx(ctx context.Context, d time.Duration) bool {
	if d <= 0 {
		return ctx.Err() == nil
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

func parseRetryAfter(body []byte, h http.Header) time.Duration {
	var b struct {
		RetryAfter float64 `json:"retry_after"`
	}
	if len(body) > 0 {
		_ = json.Unmarshal(body, &b)
	}
	if b.RetryAfter > 0 {
		return time.Duration(b.RetryAfter * float64(time.Second))
	}
	if v := h.Get("Retry-After"); v != "" {
		if secs, err := strconv.ParseFloat(v, 64); err == nil {
			return time.Duration(secs * float64(time.Second))
		}
	}
	return time.Second
}

// parseAPIErrorMessage pulls Discord's human-readable error text out of a
// response body (e.g. {"message":"Missing Permissions","code":50013}). Returns
// "" when the body has none.
func parseAPIErrorMessage(body []byte) string {
	if len(body) == 0 {
		return ""
	}
	var e struct {
		Message string `json:"message"`
	}
	if json.Unmarshal(body, &e) == nil {
		return e.Message
	}
	return ""
}

// errSystemMessage is Discord's error code for acting on a system message
// (call, group rename, recipient add/remove). Packages carry these unmarked,
// so every run queues some.
const errSystemMessage = 50021

// parseAPIErrorCode extracts Discord's JSON error code, which is finer-grained
// than the HTTP status. Returns 0 when the body carries none.
func parseAPIErrorCode(body []byte) int {
	if len(body) == 0 {
		return 0
	}
	var e struct {
		Code int `json:"code"`
	}
	if json.Unmarshal(body, &e) == nil {
		return e.Code
	}
	return 0
}

func isGlobal429(body []byte, h http.Header) bool {
	if h.Get("X-RateLimit-Global") != "" || h.Get("X-RateLimit-Scope") == "global" {
		return true
	}
	var b struct {
		Global bool `json:"global"`
	}
	_ = json.Unmarshal(body, &b)
	return b.Global
}

func snippet(b []byte) string {
	s := string(b)
	if len(s) > 120 {
		return s[:120] + "…"
	}
	return s
}
