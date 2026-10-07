package feed

import (
	"os"
	"testing"

	"azzurrotech/stenella/netguard"
)

// TestMain lets the suite use loopback httptest fixtures.
//
// The engine dials through netguard, which blocks loopback/private fetch
// targets by default (SSRF guard) — the shipped binary stays blocked; see
// netguard.AllowPrivate. This flag exists ONLY so the tests below can fetch
// their own fixture servers on 127.0.0.1; nothing in the shipped code sets it.
func TestMain(m *testing.M) {
	netguard.AllowPrivate = true
	os.Exit(m.Run())
}
