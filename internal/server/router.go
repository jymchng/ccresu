package server

import (
	"net/http"
	"strings"
)

// params are the wildcard segments of a matched route, e.g. {"id": "<uuid>"}.
type params map[string]string

// handlerFunc is one endpoint. Handlers write their own response so the SSE
// endpoint can stream instead of returning a document.
type handlerFunc func(*Server, http.ResponseWriter, *http.Request, params)

// route is one method+path pair.
//
// The pattern is matched segment by segment; a segment wrapped in braces is a
// wildcard. Hand-rolling this keeps 404/405 answers in the API's JSON envelope,
// which net/http's ServeMux method patterns cannot express.
type route struct {
	method string
	parts  []string
	// mutation marks a state-changing route, which is what makes the bearer
	// token mandatory.
	mutation bool
	handler  handlerFunc
}

// buildRoutes is the complete API surface.
//
// Every state-changing route is a POST or PUT on a session id and takes an
// optional JSON body of data, never a command: there is no endpoint that accepts
// something to execute.
func (s *Server) buildRoutes() []route {
	return []route{
		{method: http.MethodGet, parts: []string{"health"}, handler: (*Server).handleHealth},
		{method: http.MethodGet, parts: []string{"version"}, handler: (*Server).handleVersion},
		{method: http.MethodGet, parts: []string{"status"}, handler: (*Server).handleStatus},
		{method: http.MethodGet, parts: []string{"sessions"}, handler: (*Server).handleSessions},
		{method: http.MethodGet, parts: []string{"sessions", "{id}"}, handler: (*Server).handleSession},
		{method: http.MethodGet, parts: []string{"sessions", "{id}", "history"}, handler: (*Server).handleHistory},
		{method: http.MethodPost, parts: []string{"sessions", "{id}", "recover"}, mutation: true, handler: (*Server).handleRecover},
		{method: http.MethodPost, parts: []string{"sessions", "{id}", "enable"}, mutation: true, handler: (*Server).handleEnable},
		{method: http.MethodPost, parts: []string{"sessions", "{id}", "disable"}, mutation: true, handler: (*Server).handleDisable},
		{method: http.MethodGet, parts: []string{"recoveries"}, handler: (*Server).handleRecoveries},
		{method: http.MethodGet, parts: []string{"events"}, handler: (*Server).handleEvents},
		{method: http.MethodGet, parts: []string{"settings"}, handler: (*Server).handleSettings},
		{method: http.MethodPut, parts: []string{"settings"}, mutation: true, handler: (*Server).handleUpdateSetting},
		{method: http.MethodGet, parts: []string{"telegram", "status"}, handler: (*Server).handleTelegram},
		{method: http.MethodGet, parts: []string{"config"}, handler: (*Server).handleConfig},
		{method: http.MethodPost, parts: []string{"test"}, mutation: true, handler: (*Server).handleTest},
	}
}

// splitPath breaks a request path into segments, ignoring the API prefix and any
// trailing slash.
func splitPath(path string) ([]string, bool) {
	if !strings.HasPrefix(path, APIPrefix) {
		return nil, false
	}
	rest := strings.TrimPrefix(path, APIPrefix)
	rest = strings.Trim(rest, "/")
	if rest == "" {
		return nil, true
	}
	return strings.Split(rest, "/"), true
}

// match finds the route for method and segments.
//
// It returns the allowed methods for the path when the path exists but the method
// does not, so the caller can answer 405 with a correct Allow header instead of a
// misleading 404.
func (s *Server) match(method string, segments []string) (route, params, []string, bool) {
	var allowed []string
	for _, candidate := range s.routes {
		if len(candidate.parts) != len(segments) {
			continue
		}
		matched := make(params, 2)
		ok := true
		for i, part := range candidate.parts {
			if strings.HasPrefix(part, "{") && strings.HasSuffix(part, "}") {
				name := strings.TrimSuffix(strings.TrimPrefix(part, "{"), "}")
				if segments[i] == "" {
					ok = false
					break
				}
				matched[name] = segments[i]
				continue
			}
			if part != segments[i] {
				ok = false
				break
			}
		}
		if !ok {
			continue
		}
		if candidate.method == method {
			return candidate, matched, nil, true
		}
		allowed = append(allowed, candidate.method)
	}
	return route{}, nil, allowed, false
}
