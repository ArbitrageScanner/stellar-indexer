package queue

import (
	"testing"

	"github.com/stellar/go-stellar-sdk/network"
)

func TestKeyRoundTrip(t *testing.T) {
	for _, seq := range []uint32{0, 1, 65312802, ^uint32(0)} {
		got, err := SeqOf(key(seq))
		if err != nil || got != seq {
			t.Fatalf("%d -> %d %v", seq, got, err)
		}
	}
	if _, err := SeqOf([]byte{1, 2}); err == nil {
		t.Fatal("short key accepted")
	}
	if _, err := SeqOf([]byte{1, 0, 0, 0, 0, 0, 0, 0}); err == nil {
		t.Fatal("key beyond uint32 accepted")
	}
}

func TestNetworkID(t *testing.T) {
	a, b := NetworkID(network.PublicNetworkPassphrase), NetworkID(network.TestNetworkPassphrase)
	if a == b || len(a) != 16 || a != NetworkID(network.PublicNetworkPassphrase) {
		t.Fatalf("network ids %q %q", a, b)
	}
}
