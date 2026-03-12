// Package mdns advertises the opencapy daemon on the local network via
// mDNS / Bonjour so the iOS app can discover and connect directly when
// both devices are on the same Wi-Fi — no relay hop needed.
//
// Service type: _opencapy._tcp
// TXT records:  token=<relay-token>  version=1
//
// The relay token lets iOS match the discovered LAN service to the
// already-paired relay machine, enabling transparent fallback.
package mdns

import (
	"log"

	"github.com/grandcat/zeroconf"
)

// Publisher holds the active mDNS registration.
type Publisher struct {
	server *zeroconf.Server
}

// Publish registers "_opencapy._tcp" on the local network.
// name is the display name (hostname). token is the relay pairing token.
// Call Stop() to deregister.
func Publish(name, token string, port int) (*Publisher, error) {
	txt := []string{
		"token=" + token,
		"version=1",
	}
	server, err := zeroconf.Register(name, "_opencapy._tcp", "local.", port, txt, nil)
	if err != nil {
		return nil, err
	}
	log.Printf("[mDNS] advertising \"%s\" on port %d", name, port)
	return &Publisher{server: server}, nil
}

// Stop deregisters the mDNS service.
func (p *Publisher) Stop() {
	if p.server != nil {
		p.server.Shutdown()
		log.Printf("[mDNS] stopped advertising")
	}
}
