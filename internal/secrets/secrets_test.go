package secrets

import (
	"errors"
	"testing"
)

func TestRoundTrip(t *testing.T) {
	MockInit()
	if err := Set("test-profile", "s3cret"); err != nil {
		t.Fatal(err)
	}
	pw, err := Get("test-profile")
	if err != nil {
		t.Fatal(err)
	}
	if pw != "s3cret" {
		t.Fatalf("got %q", pw)
	}
	if err := Delete("test-profile"); err != nil {
		t.Fatal(err)
	}
	if _, err := Get("test-profile"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("want ErrNotFound after delete, got %v", err)
	}
}

func TestGetMissingIsErrNotFound(t *testing.T) {
	MockInit()
	_, err := Get("never-stored")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
}

func TestDeleteMissingIsNotAnError(t *testing.T) {
	MockInit()
	if err := Delete("never-stored"); err != nil {
		t.Fatalf("deleting a missing entry must be a no-op, got %v", err)
	}
}

func TestStoreUnavailableSurfacesError(t *testing.T) {
	storeErr := errors.New("no keyring daemon")
	MockInitWithError(storeErr)
	t.Cleanup(MockInit) // restore working mock for later tests
	if err := Set("p", "x"); !errors.Is(err, storeErr) {
		t.Fatalf("Set: want store error, got %v", err)
	}
	if _, err := Get("p"); err == nil {
		t.Fatal("Get: want error from unavailable store")
	}
}

func TestStoreName(t *testing.T) {
	if StoreName() == "" {
		t.Fatal("StoreName must be non-empty for user-facing messages")
	}
}
