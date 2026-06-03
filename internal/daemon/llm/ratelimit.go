package llm

// Provider-agnostic rate limiter controlled by VIX_REQUESTS_PER_MINUTE.
// When set, all LLM providers in the process are throttled to that cap via a
// sliding-window counter. Disabled by default (0 = no limit).

import (
	"context"
	"log"
	"os"
	"strconv"
	"sync"
	"time"

	"github.com/kirby88/vix/internal/config"
)

var globalRPM = func() int {
	v := os.Getenv("VIX_REQUESTS_PER_MINUTE")
	if v == "" {
		return 0
	}
	n, _ := strconv.Atoi(v)
	if n <= 0 {
		return 0
	}
	log.Printf("[llm] rate limiter enabled: %d req/min (VIX_REQUESTS_PER_MINUTE)", n)
	return n
}()

var (
	rpmMu       sync.Mutex
	rpmWindowed []time.Time
)

// waitForRPMSlot blocks until a request slot is available. No-op when
// VIX_REQUESTS_PER_MINUTE is unset or zero.
func waitForRPMSlot(ctx context.Context) error {
	if globalRPM == 0 {
		return nil
	}
	const window = time.Minute
	for {
		rpmMu.Lock()
		now := time.Now()
		cutoff := now.Add(-window)
		i := 0
		for i < len(rpmWindowed) && rpmWindowed[i].Before(cutoff) {
			i++
		}
		rpmWindowed = rpmWindowed[i:]
		if len(rpmWindowed) < globalRPM {
			rpmWindowed = append(rpmWindowed, now)
			rpmMu.Unlock()
			return nil
		}
		waitUntil := rpmWindowed[0].Add(window)
		wait := time.Until(waitUntil)
		rpmMu.Unlock()
		log.Printf("[llm] rate slot full (%d req/min), waiting %v", globalRPM, wait.Round(time.Second))
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(wait):
		}
	}
}

// rateLimitedClient wraps any Client and applies the global RPM cap before
// each call. Used by NewFromModel when VIX_REQUESTS_PER_MINUTE > 0.
type rateLimitedClient struct{ inner Client }

func (r *rateLimitedClient) Provider() ProviderID          { return r.inner.Provider() }
func (r *rateLimitedClient) Model() string                 { return r.inner.Model() }
func (r *rateLimitedClient) Credential() config.Credential { return r.inner.Credential() }
func (r *rateLimitedClient) MaxTokens() int64              { return r.inner.MaxTokens() }
func (r *rateLimitedClient) Effort() string                { return r.inner.Effort() }

func (r *rateLimitedClient) StreamMessage(ctx context.Context, system []SystemBlock, messages []MessageParam, tools []ToolParam, onDelta func(string), onThinkingDelta func(string)) (*Message, time.Duration, error) {
	if err := waitForRPMSlot(ctx); err != nil {
		return nil, 0, err
	}
	return r.inner.StreamMessage(ctx, system, messages, tools, onDelta, onThinkingDelta)
}

func (r *rateLimitedClient) StreamMessageWith(ctx context.Context, system []SystemBlock, messages []MessageParam, tools []ToolParam, onDelta func(string), onThinkingDelta func(string), opts StreamOpts) (*Message, time.Duration, error) {
	if err := waitForRPMSlot(ctx); err != nil {
		return nil, 0, err
	}
	return r.inner.StreamMessageWith(ctx, system, messages, tools, onDelta, onThinkingDelta, opts)
}
