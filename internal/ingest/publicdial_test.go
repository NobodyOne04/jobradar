package ingest_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"

	"github.com/NobodyOne04/jobradar/internal/ingest"
)

func TestPublicAddr(t *testing.T) {
	for addr, public := range map[string]bool{
		"8.8.8.8": true, "2606:4700::1111": true,
		"127.0.0.1": false, "10.1.2.3": false, "172.18.0.5": false, "192.168.1.1": false,
		"169.254.169.254": false, "100.64.0.1": false, "0.0.0.0": false, "::1": false,
		"fe80::1": false, "fd00::1": false, "224.0.0.1": false, "::ffff:127.0.0.1": false,
		"0.1.2.3": false, "198.18.0.1": false, "192.0.0.8": false, "240.0.0.1": false,
		"255.255.255.255": false, "::7f00:1": false, "64:ff9b::7f00:1": false, "64:ff9b:1::a00:1": false,
		"2002:7f00:1::": false, "2001:0:4136:e378::1": false, "2001:4860:4860::8888": true,
	} {
		if got := ingest.PublicAddr(netip.MustParseAddr(addr)); got != public {
			t.Errorf("%s: public = %v, want %v", addr, got, public)
		}
	}
}

func TestPublicClientRefusesInternalTargets(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("secret")) }))
	defer srv.Close()
	c := ingest.NewPublicHTTPClient(1000)
	c.Attempts = 1
	// httptest listens on 127.0.0.1: the dial itself is refused, whatever name led there.
	for _, u := range []string{srv.URL, strings.Replace(srv.URL, "127.0.0.1", "localhost", 1)} {
		if body, err := c.GetBody(context.Background(), u); err == nil || !strings.Contains(err.Error(), "not a public address") {
			t.Errorf("%s: body %q, err %v; want refused", u, body, err)
		}
	}
}
