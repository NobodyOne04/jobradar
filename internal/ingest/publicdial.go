package ingest

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"syscall"
	"time"
)

// errNotPublic is returned for a connection to an address that is not on the public internet.
var errNotPublic = errors.New("refused: not a public address")

// cgnat is the carrier-grade NAT range (RFC 6598), which netip does not classify as private.
var cgnat = netip.MustParsePrefix("100.64.0.0/10")

// PublicAddr reports whether ip is a public unicast address: not loopback, private,
// link-local, CGNAT, multicast or unspecified.
func PublicAddr(ip netip.Addr) bool {
	ip = ip.Unmap()
	return ip.IsValid() && ip.IsGlobalUnicast() && !ip.IsPrivate() && !cgnat.Contains(ip)
}

// NewPublicHTTPClient is NewHTTPClient for sources whose host comes from user input (a
// Teamtailor career site, ADR 0014). Every connection is checked after DNS resolution, so a
// public-looking name that resolves (or is rebound) to an internal address, or a redirect to
// one, is refused; redirects must also stay on https. No proxy: the check sees the target.
func NewPublicHTTPClient(perSecond float64) *HTTPClient {
	c := NewHTTPClient(perSecond)
	dialer := &net.Dialer{
		Timeout: 10 * time.Second,
		Control: func(_, address string, _ syscall.RawConn) error {
			ap, err := netip.ParseAddrPort(address)
			if err != nil || !PublicAddr(ap.Addr()) {
				return fmt.Errorf("%w: %s", errNotPublic, address)
			}
			return nil
		},
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	transport.DialContext = dialer.DialContext
	c.HTTP = &http.Client{
		Timeout:   c.HTTP.Timeout,
		Transport: transport,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if req.URL.Scheme != "https" || len(via) >= 5 {
				return fmt.Errorf("redirect to %s refused (https only, at most 5)", req.URL.Redacted())
			}
			return nil
		},
	}
	return c
}
