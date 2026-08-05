// Package remoteurl defines the resolved HTTPS endpoint contract shared by
// untrusted URL policy and network command adapters.
package remoteurl

import (
	"context"
	"net/netip"
)

// Endpoint freezes one canonical HTTPS URL and the public addresses permitted
// for a single network command.
type Endpoint struct {
	URL       string
	Hostname  string
	Port      uint16
	Addresses []netip.Addr
}

// Policy re-resolves and validates a remote immediately before network I/O.
type Policy interface {
	ResolveHTTPSRemote(context.Context, string) (Endpoint, error)
}
