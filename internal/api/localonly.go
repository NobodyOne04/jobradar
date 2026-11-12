package api

import (
	"net"
	"net/http"
	"net/url"
	"strings"
)

// withLocalOnly protects an API that has no auth and listens on localhost (Q2) from the
// browser you use for everything else:
//   - DNS rebinding: a page on evil.example re-pointed to 127.0.0.1 sends Host:
//     evil.example, so any Host outside allowedHosts is rejected.
//   - CSRF: browsers send Origin on cross-site POST, PATCH and DELETE, so a request whose
//     Origin is not an allowed host is rejected.
//
// curl and other non-browser clients send no Origin and use localhost, so they pass.
func withLocalOnly(allowedHosts []string, next http.Handler) http.Handler {
	allowed := map[string]bool{}
	for _, h := range allowedHosts {
		allowed[strings.ToLower(h)] = true
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !allowed[hostname(r.Host)] {
			WriteProblem(w, http.StatusForbidden, "host not allowed", "")
			return
		}
		if origin := r.Header.Get("Origin"); origin != "" {
			u, err := url.Parse(origin)
			if err != nil || !allowed[strings.ToLower(u.Hostname())] {
				WriteProblem(w, http.StatusForbidden, "cross-origin request refused", "")
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

// hostname strips the port from a Host header: "localhost:8080" -> "localhost",
// "[::1]:8080" -> "::1".
func hostname(hostport string) string {
	if h, _, err := net.SplitHostPort(hostport); err == nil {
		hostport = h
	}
	return strings.ToLower(strings.Trim(hostport, "[]"))
}
