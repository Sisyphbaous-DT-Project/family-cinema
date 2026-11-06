package main

import "testing"

func TestDiscoveryFollowsListenAddress(t *testing.T) {
	for address, want := range map[string]bool{
		"": true, "0.0.0.0": true, "::": true, "192.168.1.20": true,
		"127.0.0.1": false, "127.0.0.2": false, "::1": false, "localhost": false,
	} {
		if got := discoveryAllowed(address); got != want {
			t.Errorf("discoveryAllowed(%q) = %v, want %v", address, got, want)
		}
	}
}
