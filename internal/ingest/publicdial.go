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

// special lists the IANA special-purpose ranges that netip's checks miss: "this network",
// CGNAT, IETF protocol assignments, benchmarking, reserved, and the IPv6 transition ranges
// that embed an IPv4 address (IPv4-compatible, NAT64, 6to4, Teredo), refused whole.
var special = func() []netip.Prefix {
	var out []netip.Prefix
	for _, p := range []string{
		"0.0.0.0/8", "100.64.0.0/10", "192.0.0.0/24", "192.0.2.0/24", "198.18.0.0/15",
		"198.51.100.0/24", "203.0.113.0/24", "240.0.0.0/4",
		"::/96", "64:ff9b::/96", "64:ff9b:1::/48", "2001::/32", "2001:db8::/32", "2002::/16",
	} {
		out = append(out, netip.MustParsePrefix(p))
	}
	return out
}()

// PublicAddr reports whether ip is a public unicast address: not loopback, private,
// link-local, multicast, unspecified, or one of the special ranges above.
func PublicAddr(ip netip.Addr) bool {
	ip = ip.Unmap()
	if !ip.IsValid() || !ip.IsGlobalUnicast() || ip.IsPrivate() {
		return false
	}
	for _, p := range special {
		if p.Contains(ip) {
			return false
		}
	}
	return true
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
