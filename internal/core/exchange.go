package core

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"time"
)

// Observer sees the messages of an exchange as they pass.
type Observer interface {
	// Request is called before each Request is sent; retry marks the
	// Request repeated with Digest credentials.
	Request(r *Request, retry bool) error
	// Sent is called once a request is Sent and its response's head read.
	Sent(r *Request) error
	// Response is called with each Response shown: the last, and the
	// redirects that --all or --verbose ask for.
	Response(r *http.Response, headers []Header, start time.Time) error
}

type RedirectLimitError int

func (e RedirectLimitError) Error() string {
	return fmt.Sprintf("too many redirects (--max-redirects=%d)", int(e))
}

// Exchange is a request built from arguments, with its session and
// authentication, and the transport that sends it.
type Exchange struct {
	options   Options
	Request   *Request
	session   *session
	auth      authentication
	transport *transport
	body      io.Closer
	cleanup   func()
	closed    bool
}

func Prepare(o Options, program string, stdin io.Reader, stdinTTY bool) (*Exchange, error) {
	req, cleanup, err := buildRequest(o, program, stdin, stdinTTY)
	if err != nil {
		return nil, err
	}
	e := &Exchange{options: o, Request: req, body: req.Body, cleanup: cleanup, session: &session{cookies: []storedCookie{}}}
	if err = e.authenticate(); err != nil {
		e.Close()
		return nil, err
	}
	return e, nil
}

func (e *Exchange) authenticate() (err error) {
	o, req := e.options, e.Request
	if name := o.Value("session"); name != "" || o.Has("session-read-only") {
		if name == "" {
			name = o.Value("session-read-only")
		}
		if e.session, err = loadSession(req.Host, name); err != nil {
			return err
		}
		e.session.apply(req, o)
	}
	s := e.session
	if e.auth, err = authCredentials(o, req); err != nil {
		return err
	}
	if s.path != "" {
		if e.auth.kind == "" {
			e.auth = s.credentials()
		} else {
			s.auth = map[string]any{"type": e.auth.kind, "raw_auth": e.auth.raw}
		}
		if err = s.updateHeaders(req, o); err != nil {
			return err
		}
	}
	if e.auth.kind == "" && !o.Has("ignore-netrc") {
		if user, password, ok := netrcAuth(req.URL.Hostname()); ok {
			e.auth = authentication{kind: "basic", username: user, password: password, latin1: true}
		}
	}
	if err = applyAuth(req, e.auth); err != nil {
		return err
	}
	if kind := e.auth.kind; kind != "" && kind != "basic" && kind != "digest" && kind != "bearer" {
		return fmt.Errorf("unsupported session authentication type: %s", kind)
	}
	s.addCookies(req)
	return nil
}

// SetCookies gives the exchange a jar in place of its session's, sending
// the cookies that match unless the request has its own Cookie header.
func (e *Exchange) SetCookies(cookies []Cookie) {
	e.session.cookies = slices.Clone(cookies)
	e.session.addCookies(e.Request)
}

// Cookies returns the jar as the exchange left it.
func (e *Exchange) Cookies() []Cookie {
	return e.session.cookies
}

// Open sets up the transport, apart from prepare so that the CLI's update
// warnings keep their place between the two.
func (e *Exchange) Open() (err error) {
	e.transport, err = newTransport(e.options)
	return err
}

// Close disconnects first, so that closing an unread response body after
// it does not drain the rest from the network.
func (e *Exchange) Close() {
	if e.closed {
		return
	}
	e.closed = true
	if e.transport != nil {
		e.transport.Close()
		e.transport.tls.close()
	}
	if e.body != nil {
		_ = e.body.Close()
	}
	e.cleanup()
}

func (e *Exchange) SaveSession() error {
	if e.session.path == "" || e.options.Has("session-read-only") && e.session.exists {
		return nil
	}
	return e.session.save()
}

// Do sends the request, follows redirects and answers a Digest challenge
// as the options ask, and returns the last response with its body unread.
func (e *Exchange) Do(ctx context.Context, obs Observer) (*http.Response, error) {
	o, t, r, s, auth := e.options, e.transport, e.Request, e.session, e.auth
	follow := o.Has("follow") || o.Has("download")
	history := o.Has("all") || o.Has("verbose")
	redirects := 0
	digestTried := false
	for {
		if err := obs.Request(r, digestTried); err != nil {
			return nil, err
		}
		start := time.Now()
		response, headers, err := t.send(ctx, r)
		if err != nil {
			return nil, err
		}
		if err = obs.Sent(r); err != nil {
			t.Close()
			response.Body.Close()
			return nil, err
		}
		s.receiveCookies(response.Cookies(), r.URL)
		redirect := response.Header.Get("Location") != "" && (response.StatusCode == 301 || response.StatusCode == 302 || response.StatusCode == 303 || response.StatusCode == 307 || response.StatusCode == 308)
		challenge, offered := digestChallenge(response.Header.Values("WWW-Authenticate"))
		digest := auth.kind == "digest" && !digestTried && response.StatusCode == 401 && offered
		if redirect {
			redirects++
			if max := o.Integer("max-redirects"); max != 0 && redirects == max {
				t.Close()
				response.Body.Close()
				return nil, RedirectLimitError(max)
			}
		}
		redirect = follow && redirect
		if !digest && (history || !redirect) {
			if err = obs.Response(response, headers, start); err != nil {
				t.Close()
				response.Body.Close()
				return nil, err
			}
		}
		if !redirect && !digest {
			return response, nil
		}
		_, err = io.Copy(io.Discard, response.Body)
		response.Body.Close()
		if err != nil {
			return nil, err
		}
		if response.Close {
			t.Close()
		}
		if digest {
			value, e := digestAuthorization(challenge, r.Method, r.URL.RequestURI(), auth)
			if e != nil {
				return nil, e
			}
			r.SetHeader("Authorization", value)
			digestTried = true
		} else {
			digestTried = false
			next, e := r.URL.Parse(response.Header.Get("Location"))
			if e != nil {
				return nil, e
			}
			if next.Scheme != "http" && next.Scheme != "https" {
				return nil, fmt.Errorf("unsupported redirect scheme: %s", next.Scheme)
			}
			stripAuth := address(r.URL) != address(next) || r.URL.Scheme != next.Scheme
			if r.URL.Hostname() == next.Hostname() && r.URL.Scheme == "http" && next.Scheme == "https" && (r.URL.Port() == "" || r.URL.Port() == "80") && (next.Port() == "" || next.Port() == "443") {
				stripAuth = false
			}
			if stripAuth {
				r.deleteHeader("Authorization")
				auth.kind = ""
			}
			if user, password, ok := netrcAuth(next.Hostname()); ok {
				auth = authentication{kind: "basic", username: user, password: password, latin1: true}
				if err := applyAuth(r, auth); err != nil {
					return nil, err
				}
			}
			status := response.StatusCode
			if status == 303 && r.Method != "HEAD" || status == 302 && r.Method != "HEAD" || status == 301 && r.Method == "POST" {
				r.Method = "GET"
			}
			if status != 307 && status != 308 {
				r.Body = nil
				r.streamedBody = false
				r.liveInput = false
				r.GetBody = func() (io.ReadCloser, error) { return io.NopCloser(strings.NewReader("")), nil }
				r.ContentLength = 0
				r.TransferEncoding = nil
				for _, name := range []string{"Content-Length", "Content-Type", "Transfer-Encoding"} {
					r.deleteHeader(name)
				}
			}
			r.URL = next
			if r.defaultHost {
				r.SetHeader("Host", next.Host)
			}
		}
		if !digest {
			r.deleteHeader("Cookie")
		}
		s.addCookies(r)
		if r.ContentLength != 0 || len(r.TransferEncoding) > 0 {
			r.Body, err = r.GetBody()
			if err != nil {
				return nil, err
			}
		}
	}
}
