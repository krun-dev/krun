package upstream

import (
	"context"
	"errors"
	"net"
	"net/http"
	"strings"
	"testing"
)

func TestTransportAppliesPoliciesBeforeDial(t *testing.T) {
	transport := NewTransport(Policy{
		CheckHost: func(host, port string) error { return errors.New("host denied") },
		CheckIP:   func(net.IP) error { return nil },
	})
	if _, err := transport.DialContext(context.Background(), "tcp", "127.0.0.1:80"); err == nil || !strings.Contains(err.Error(), "host denied") {
		t.Fatalf("host policy was not applied: %v", err)
	}

	transport = NewTransport(Policy{
		CheckHost: func(host, port string) error { return nil },
		CheckIP:   func(net.IP) error { return errors.New("address denied") },
	})
	if _, err := transport.DialContext(context.Background(), "tcp", "127.0.0.1:80"); err == nil || !strings.Contains(err.Error(), "address denied") {
		t.Fatalf("address policy was not applied: %v", err)
	}
}

func TestRejectRedirect(t *testing.T) {
	if err := RejectRedirect(&http.Request{}, nil); !errors.Is(err, ErrRedirect) {
		t.Fatalf("redirect error = %v", err)
	}
}
