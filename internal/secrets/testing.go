package secrets

import "github.com/zalando/go-keyring"

// MockInit swaps the real OS credential store for an in-memory mock for
// the rest of the process lifetime. Test helper only — production code
// must never call this.
func MockInit() {
	keyring.MockInit()
}

// MockInitWithError makes every credential-store operation fail with err,
// simulating a headless system with no keyring. Test helper only.
func MockInitWithError(err error) {
	keyring.MockInitWithError(err)
}
