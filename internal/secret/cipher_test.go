package secret

import "testing"

func TestRoundTrip(t *testing.T) {
	c, err := LoadOrCreate(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	encrypted, err := c.Encrypt("center-password")
	if err != nil {
		t.Fatal(err)
	}
	if encrypted == "center-password" {
		t.Fatal("secret was stored as plaintext")
	}
	plain, err := c.Decrypt(encrypted)
	if err != nil || plain != "center-password" {
		t.Fatalf("round trip failed: %q %v", plain, err)
	}
}
