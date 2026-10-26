package ble

import "testing"

func TestDiagnosticDiscoveryIsSeparateFromNormalPairing(t *testing.T) {
	for _, diagnostic := range []bool{false, true} {
		for _, advertised := range []bool{false, true} {
			m := message{Event: "peer", Version: WireVersion, ID: "00000000000000000000000000000001", Name: "peer", Pairing: true, Diagnostic: advertised}
			peer, ok := discoveredPeer(m, diagnostic)
			if ok != (diagnostic == advertised) || ok && peer.Diagnostic != diagnostic {
				t.Fatal("normal and diagnostic clients were mixed")
			}
			m.Version++
			if _, ok := discoveredPeer(m, diagnostic); ok {
				t.Fatal("accepted unsupported protocol")
			}
		}
	}
}
