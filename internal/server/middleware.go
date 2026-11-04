package server

import (
	"fmt"
	"net"
	"net/http"
	"runtime/debug"
	"strconv"
	"strings"
	"time"
)

// responseRecorder records the status code for the request log and, importantly,
// keeps the http.Flusher contract intact so the SSE endpoint can still flush.
type responseRecorder struct {
	http.ResponseWriter
	status int
	wrote  bool
}

// WriteHeader records and forwards the status.
func (r *responseRecorder) WriteHeader(status int) {
	if !r.wrote {
		r.status = status
		r.wrote = true
	}
	r.ResponseWriter.WriteHeader(status)
}

// Write records an implicit 200 and forwards the bytes.
func (r *responseRecorder) Write(body []byte) (int, error) {
	if !r.wrote {
		r.status = http.StatusOK
		r.wrote = true
	}
	return r.ResponseWriter.Write(body)
}

// Flush forwards to the underlying writer when it can flush.
func (r *responseRecorder) Flush() {
	if flusher, ok := r.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

// Unwrap exposes the original writer to http.ResponseController.
func (r *responseRecorder) Unwrap() http.ResponseWriter { return r.ResponseWriter }

// ServeHTTP is the whole request pipeline.
//
// The order matters: hardening headers are set before anything can fail, the
// limiter runs before the token check (so guessing a token is expensive), the
// Origin allowlist is checked before any handler sees the request, and the token
// is verified only for state-changing methods.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	start := s.cfg.Clock.Now()
	recorder := &responseRecorder{ResponseWriter: w, status: http.StatusOK}
	defer func() {
		if recovered := recover(); recovered != nil {
			s.cfg.Log.Error("api handler panicked",
				"method", r.Method, "path", r.URL.Path, "panic", recovered,
				"stack", string(debug.Stack()))
			if !recorder.wrote {
				s.writeError(recorder, errInternal())
			}
		}
		s.cfg.Log.Debug("api request",
			"method", r.Method, "path", r.URL.Path, "status", recorder.status,
			"duration", s.cfg.Clock.Now().Sub(start).String())
	}()

	s.setSecureHeaders(recorder)

	segments, prefixed := splitPath(r.URL.Path)
	if !prefixed {
		s.writeError(recorder, errNotFound(fmt.Sprintf("no such endpoint; the API lives under %s", APIPrefix)))
		return
	}

	if allowed, wait := s.limiter.allow(clientKey(r), start); !allowed {
		seconds := int(wait.Seconds())
		if seconds < 1 {
			seconds = 1
		}
		recorder.Header().Set("Retry-After", strconv.Itoa(seconds))
		s.writeError(recorder, &apiError{
			Status:  http.StatusTooManyRequests,
			Code:    codeRateLimited,
			Message: fmt.Sprintf("too many requests; retry in %ds", seconds),
		})
		return
	}

	if apiErr := s.applyCORS(recorder, r); apiErr != nil {
		s.writeError(recorder, apiErr)
		return
	}
	if r.Method == http.MethodOptions {
		// A preflight that reached this point passed the Origin check.
		recorder.WriteHeader(http.StatusNoContent)
		return
	}

	matched, pathParams, allowedMethods, ok := s.match(r.Method, segments)
	if !ok {
		if len(allowedMethods) > 0 {
			recorder.Header().Set("Allow", strings.Join(allowedMethods, ", "))
			s.writeError(recorder, &apiError{
				Status:  http.StatusMethodNotAllowed,
				Code:    codeMethod,
				Message: fmt.Sprintf("%s is not allowed on this endpoint", r.Method),
				Details: []string{"allowed: " + strings.Join(allowedMethods, ", ")},
			})
			return
		}
		s.writeError(recorder, errNotFound("no such endpoint"))
		return
	}

	if matched.mutation {
		if apiErr := s.checkMutationRequest(recorder, r); apiErr != nil {
			s.writeError(recorder, apiErr)
			return
		}
		if apiErr := s.authenticate(r); apiErr != nil {
			s.writeError(recorder, apiErr)
			return
		}
	}

	matched.handler(s, recorder, r, pathParams)
}

// setSecureHeaders applies the hardening headers every response carries.
//
// The API serves JSON to scripts, so the policy can be maximally restrictive: no
// scripts, no framing, no referrer, no caching of credentials or state.
func (s *Server) setSecureHeaders(w http.ResponseWriter) {
	header := w.Header()
	header.Set("X-Content-Type-Options", "nosniff")
	header.Set("X-Frame-Options", "DENY")
	header.Set("Referrer-Policy", "no-referrer")
	header.Set("Content-Security-Policy", "default-src 'none'; frame-ancestors 'none'; base-uri 'none'; form-action 'none'")
	header.Set("Cross-Origin-Resource-Policy", "same-origin")
	header.Set("Cross-Origin-Opener-Policy", "same-origin")
	header.Set("Cache-Control", "no-store")
}

// applyCORS answers the Origin allowlist.
//
// A request without an Origin is not a browser request (curl, a script, the CLI),
// so it is allowed: the token is what protects writes and the listener is
// loopback. A browser request must name an allowlisted origin, which stops a
// hostile page from reading the API through the user's browser.
func (s *Server) applyCORS(w http.ResponseWriter, r *http.Request) *apiError {
	origin := strings.TrimSpace(r.Header.Get("Origin"))
	if origin == "" {
		return nil
	}
	if !s.originAllowed(origin) {
		return &apiError{
			Status:  http.StatusForbidden,
			Code:    codeOriginForbidden,
			Message: "this origin is not in server.allowed_origins",
			Details: []string{"origin: " + origin},
		}
	}
	header := w.Header()
	// Echo the exact origin rather than "*" so a response is never cacheable for
	// another origin, and never advertise credentials: the token is a header the
	// caller sets explicitly, not a cookie the browser attaches by itself.
	header.Set("Access-Control-Allow-Origin", origin)
	header.Add("Vary", "Origin")
	header.Set("Access-Control-Allow-Methods", "GET, POST, PUT, OPTIONS")
	header.Set("Access-Control-Allow-Headers", "Authorization, Content-Type")
	header.Set("Access-Control-Max-Age", "600")
	return nil
}

// originAllowed compares an origin against the allowlist, case-insensitively and
// ignoring a trailing slash.
func (s *Server) originAllowed(origin string) bool {
	candidate := strings.ToLower(strings.TrimSuffix(strings.TrimSpace(origin), "/"))
	for _, allowed := range s.cfg.AllowedOrigins {
		if candidate == allowed {
			return true
		}
	}
	return false
}

// checkMutationRequest enforces the body contract for state-changing requests:
// a bounded body of JSON only.
//
// Rejecting form encodings matters even though the token already blocks HTML
// form posts: it keeps the surface at exactly one media type, so no parser other
// than encoding/json ever sees a request body.
func (s *Server) checkMutationRequest(w http.ResponseWriter, r *http.Request) *apiError {
	contentType := strings.ToLower(strings.TrimSpace(r.Header.Get("Content-Type")))
	hasBody := r.ContentLength != 0
	if hasBody && contentType != "" && !strings.HasPrefix(contentType, "application/json") {
		return &apiError{
			Status:  http.StatusUnsupportedMediaType,
			Code:    codeUnsupportedType,
			Message: "request bodies must be application/json",
			Details: []string{"content-type: " + contentType},
		}
	}
	if r.ContentLength > s.cfg.MaxBodyBytes {
		return &apiError{
			Status:  http.StatusRequestEntityTooLarge,
			Code:    codeTooLarge,
			Message: fmt.Sprintf("request body must be at most %d bytes", s.cfg.MaxBodyBytes),
		}
	}
	if hasBody {
		r.Body = http.MaxBytesReader(w, r.Body, s.cfg.MaxBodyBytes)
	}
	return nil
}

// clientKey names the rate-limit bucket for a request.
//
// Only the transport address is used. X-Forwarded-For is ignored on purpose: the
// API is not meant to sit behind a proxy, and trusting the header would let any
// client choose its own bucket and bypass the limit.
func clientKey(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// uptime reports how long the API has been serving.
func (s *Server) uptime() time.Duration {
	return s.cfg.Clock.Now().UTC().Sub(s.started)
}
