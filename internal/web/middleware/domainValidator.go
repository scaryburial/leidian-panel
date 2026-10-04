// Package middleware provides HTTP middleware functions for the 3x-ui web panel,
// including domain validation utilities.
package middleware

import (
	"net"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
)

// normalizeRequestHost lowercases the request host and strips the port (and the
// IPv6 brackets and a trailing root dot), so that "1.2.3.4:2096", "[::1]:2096",
// "example.com." and "example.com" can all be compared against a plain hostname
// / IP allow-list.
func normalizeRequestHost(raw string) string {
	host := strings.TrimSpace(raw)
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	host = strings.Trim(host, "[]")
	host = strings.TrimSuffix(host, ".")
	return strings.ToLower(host)
}

// DomainValidatorMiddleware returns a Gin middleware that validates the request
// host against an allow-list. Requests from any other host are rejected with
// HTTP 403 Forbidden.
//
// It accepts several allowed hosts on purpose: once the domain feature is on,
// the subscription service serves TLS with the domain certificate, and clients
// that were handed an "http(s)://<server-ip>:<sub-port>/..." subscription URL
// (the form the panel advertises before the domain exists, and the form already
// stored in clients) must keep working. Accepting only the domain silently
// turned every pre-existing IP subscription into a 403.
func DomainValidatorMiddleware(allowed ...string) gin.HandlerFunc {
	allow := make(map[string]bool, len(allowed))
	for _, a := range allowed {
		if a = strings.ToLower(strings.TrimSpace(a)); a != "" {
			allow[a] = true
		}
	}
	return func(c *gin.Context) {
		if !allow[normalizeRequestHost(c.Request.Host)] {
			c.AbortWithStatus(http.StatusForbidden)
			return
		}

		c.Next()
	}
}
