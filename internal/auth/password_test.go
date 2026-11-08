package auth

import "testing"

// nodeHash was produced by the Node panel (back/shared/password.ts) for the
// password "test1234". If this vector stops verifying, accounts created by the
// Node version can no longer log in to the Go panel.
const nodeHash = "scrypt$b511470f812db25da4a0d3b5267e5920$9b2618932363607849d36467f9d9e478389fa87bda6748e4a82a33d20108d5a432afc9430dd60159606aacb474fa6d604463dde4672e463dfc563c5d13b00682"

func TestVerifyPasswordAcceptsNodeHash(t *testing.T) {
	if !VerifyPassword("test1234", nodeHash) {
		t.Fatal("Node-generated scrypt hash must verify")
	}
	if VerifyPassword("test12345", nodeHash) {
		t.Fatal("wrong password must not verify")
	}
	if VerifyPassword("", nodeHash) {
		t.Fatal("empty password must not verify")
	}
}

func TestHashPasswordRoundTrip(t *testing.T) {
	hashed, err := HashPassword("s3cret!")
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	if !IsPasswordHash(hashed) {
		t.Fatalf("hash %q does not match the scrypt$<32hex>$<128hex> format", hashed)
	}
	if !VerifyPassword("s3cret!", hashed) {
		t.Fatal("own hash must verify")
	}
	if VerifyPassword("s3cret", hashed) {
		t.Fatal("different password must not verify")
	}
	second, err := HashPassword("s3cret!")
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	if second == hashed {
		t.Fatal("hashes must use a random salt")
	}
}

func TestVerifyPasswordLegacyPlaintext(t *testing.T) {
	if !VerifyPassword("admin", "admin") {
		t.Fatal("legacy plaintext password must verify")
	}
	if VerifyPassword("admin", "Admin") {
		t.Fatal("plaintext comparison must be case sensitive")
	}
}
