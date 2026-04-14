package resilience

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"math/rand"
	"net/http"
	"sync"
	"time"
)

type Config struct {
	MaxAttempts      int
	BaseDelay        time.Duration
	FailureThreshold int
	CoolDown         time.Duration
}

type breakerState struct {
	Failures int
	OpenedAt time.Time
}

type Policy struct {
	cfg Config
	mu  sync.Mutex
	bs  map[string]breakerState
}

func NewPolicy(cfg Config) *Policy {
	if cfg.MaxAttempts <= 0 {
		cfg.MaxAttempts = 3
	}
	if cfg.BaseDelay <= 0 {
		cfg.BaseDelay = 200 * time.Millisecond
	}
	if cfg.FailureThreshold <= 0 {
		cfg.FailureThreshold = 5
	}
	if cfg.CoolDown <= 0 {
		cfg.CoolDown = 30 * time.Second
	}
	return &Policy{cfg: cfg, bs: map[string]breakerState{}}
}

func (p *Policy) HTTPClient(service string, timeout time.Duration) *http.Client {
	base := http.DefaultTransport
	return &http.Client{
		Timeout: timeout,
		Transport: &roundTripper{
			service: service,
			base:    base,
			policy:  p,
		},
	}
}

type roundTripper struct {
	service string
	base    http.RoundTripper
	policy  *Policy
}

const maxRetryBodySnapshotBytes = 1 << 20

func (r *roundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	if err := r.policy.before(r.service); err != nil {
		return nil, err
	}
	if !retryableMethod(req.Method) {
		resp, err := r.base.RoundTrip(req)
		if err == nil && !retryableStatus(resp.StatusCode) {
			r.policy.success(r.service)
			return resp, nil
		}
		r.policy.failure(r.service)
		if err != nil {
			return nil, err
		}
		if resp.Body != nil {
			resp.Body.Close()
		}
		return nil, errors.New(resp.Status)
	}
	body, err := snapshotBody(req, maxRetryBodySnapshotBytes)
	if err != nil {
		r.policy.failure(r.service)
		return nil, err
	}
	var lastErr error
	for attempt := 1; attempt <= r.policy.cfg.MaxAttempts; attempt++ {
		clone := req.Clone(req.Context())
		clone.Body = io.NopCloser(bytes.NewReader(body))
		resp, err := r.base.RoundTrip(clone)
		if err == nil && !retryableStatus(resp.StatusCode) {
			r.policy.success(r.service)
			return resp, nil
		}
		if err == nil {
			lastErr = errors.New(resp.Status)
			if resp.Body != nil {
				resp.Body.Close()
			}
		} else {
			lastErr = err
		}
		if attempt == r.policy.cfg.MaxAttempts || !retryableMethod(req.Method) {
			break
		}
		delay := jitter(r.policy.cfg.BaseDelay, attempt)
		select {
		case <-req.Context().Done():
			return nil, req.Context().Err()
		case <-time.After(delay):
		}
	}
	r.policy.failure(r.service)
	return nil, lastErr
}

func (p *Policy) Do(ctx context.Context, service string, fn func(context.Context) error) error {
	if err := p.before(service); err != nil {
		return err
	}
	var lastErr error
	for attempt := 1; attempt <= p.cfg.MaxAttempts; attempt++ {
		lastErr = fn(ctx)
		if lastErr == nil {
			p.success(service)
			return nil
		}
		if attempt == p.cfg.MaxAttempts {
			break
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(jitter(p.cfg.BaseDelay, attempt)):
		}
	}
	p.failure(service)
	return lastErr
}

func (p *Policy) before(service string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	state := p.bs[service]
	if state.OpenedAt.IsZero() {
		return nil
	}
	if time.Since(state.OpenedAt) >= p.cfg.CoolDown {
		delete(p.bs, service)
		return nil
	}
	return errors.New("circuit breaker open for " + service)
}

func (p *Policy) success(service string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.bs, service)
}

func (p *Policy) failure(service string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	state := p.bs[service]
	state.Failures++
	if state.Failures >= p.cfg.FailureThreshold {
		state.OpenedAt = time.Now().UTC()
	}
	p.bs[service] = state
}

func snapshotBody(req *http.Request, maxBytes int64) ([]byte, error) {
	if req.Body == nil {
		return nil, nil
	}
	if maxBytes <= 0 {
		maxBytes = maxRetryBodySnapshotBytes
	}
	body, err := io.ReadAll(io.LimitReader(req.Body, maxBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(body)) > maxBytes {
		return nil, fmt.Errorf("request body exceeds retry snapshot limit of %d bytes", maxBytes)
	}
	req.Body = io.NopCloser(bytes.NewReader(body))
	return body, nil
}

func retryableMethod(method string) bool {
	switch method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return true
	default:
		return false
	}
}

func retryableStatus(status int) bool {
	return status == http.StatusTooManyRequests || status >= http.StatusInternalServerError
}

func jitter(base time.Duration, attempt int) time.Duration {
	multiplier := math.Pow(2, float64(attempt-1))
	window := float64(base) * multiplier
	return time.Duration(window/2 + rand.Float64()*window/2)
}
