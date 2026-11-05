package main

import (
	"crypto/tls"
	"net/http/httptrace"
	"sync"
	"time"

	"github.com/xingkaixin/mygo-native-ui/ui/waterfall"
)

// span is how long a request of an exchange took, a redirect or an
// answer to a Digest challenge among them, phase by phase.
type span struct {
	Name   string
	Start  time.Duration
	Phases []phase
}

type phase struct {
	Name     string
	Duration time.Duration
}

// timer keeps when the phases of each request of an exchange ended, as
// core tells the trace of the exchange's context. The dialer may tell of
// connections from goroutines of its own.
type timer struct {
	mu   sync.Mutex
	hops []*hopTimes
}

type hopTimes struct {
	name                                 string
	start, dns, connect, tls, wrote, got time.Time
}

func (tm *timer) begin(name string) {
	tm.mu.Lock()
	tm.hops = append(tm.hops, &hopTimes{name: name, start: time.Now()})
	tm.mu.Unlock()
}

func (tm *timer) mark(f func(h *hopTimes, now time.Time)) {
	tm.mu.Lock()
	if n := len(tm.hops); n > 0 {
		f(tm.hops[n-1], time.Now())
	}
	tm.mu.Unlock()
}

func (tm *timer) trace() *httptrace.ClientTrace {
	return &httptrace.ClientTrace{
		DNSDone:              func(httptrace.DNSDoneInfo) { tm.mark(func(h *hopTimes, t time.Time) { h.dns = t }) },
		ConnectDone:          func(string, string, error) { tm.mark(func(h *hopTimes, t time.Time) { h.connect = t }) },
		TLSHandshakeDone:     func(tls.ConnectionState, error) { tm.mark(func(h *hopTimes, t time.Time) { h.tls = t }) },
		WroteRequest:         func(httptrace.WroteRequestInfo) { tm.mark(func(h *hopTimes, t time.Time) { h.wrote = t }) },
		GotFirstResponseByte: func() { tm.mark(func(h *hopTimes, t time.Time) { h.got = t }) },
	}
}

// spans returns the requests of the exchange, which ended at end. A
// phase runs from the end of the one before it, so the phases of a
// request add up to how long it took.
func (tm *timer) spans(end time.Time) []span {
	tm.mu.Lock()
	defer tm.mu.Unlock()
	var out []span
	for i, h := range tm.hops {
		stop := end
		if i+1 < len(tm.hops) {
			stop = tm.hops[i+1].start
		}
		s := span{Name: h.name, Start: h.start.Sub(tm.hops[0].start)}
		last := h.start
		for _, p := range []struct {
			name string
			at   time.Time
		}{{"DNS", h.dns}, {"Connect", h.connect}, {"TLS", h.tls}, {"Send", h.wrote}, {"Wait", h.got}, {"Receive", stop}} {
			if !p.at.IsZero() && !p.at.Before(last) {
				s.Phases = append(s.Phases, phase{p.name, p.at.Sub(last)})
				last = p.at
			}
		}
		out = append(out, s)
	}
	return out
}

var timingColumns = []string{"Time"}

func waterfallSpans(spans []span) []*waterfall.Span {
	out := make([]*waterfall.Span, len(spans))
	for i, s := range spans {
		ws := &waterfall.Span{Name: s.Name, Start: s.Start}
		var total time.Duration
		for _, p := range s.Phases {
			ws.Phases = append(ws.Phases, waterfall.Phase{Name: p.Name, Duration: p.Duration})
			total += p.Duration
		}
		ws.Info = []string{total.Round(time.Millisecond / 10).String()}
		out[i] = ws
	}
	return out
}
