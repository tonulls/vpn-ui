package service

import (
	"path/filepath"
	"testing"

	"github.com/mhsanaei/3x-ui/v2/database"
	"github.com/mhsanaei/3x-ui/v2/util/crypto"
)

// TestCliSettingMutations covers the setting/credential mutations that back the
// `vpn-ui --user/--pass/--port/--path` switches: SetPort, SetBasePath,
// UpdateFirstUser, and the new SetFirstUsername — which must rename the admin
// WITHOUT touching the password (the whole point of allowing --user without --pass).
func TestCliSettingMutations(t *testing.T) {
	if err := database.InitDB(filepath.Join(t.TempDir(), "test.db")); err != nil {
		t.Fatalf("InitDB: %v", err)
	}
	settingService := SettingService{}
	userService := UserService{}

	// Port + base path round-trip (base path is normalized with leading+trailing /).
	if err := settingService.SetPort(12345); err != nil {
		t.Fatalf("SetPort: %v", err)
	}
	if p, _ := settingService.GetPort(); p != 12345 {
		t.Errorf("GetPort = %d, want 12345", p)
	}
	if err := settingService.SetBasePath("panel"); err != nil {
		t.Fatalf("SetBasePath: %v", err)
	}
	if bp, _ := settingService.GetBasePath(); bp != "/panel/" {
		t.Errorf("GetBasePath = %q, want /panel/", bp)
	}

	// Create the first admin, verify the password hashes and checks out.
	if err := userService.UpdateFirstUser("Alice", "secret-pw"); err != nil {
		t.Fatalf("UpdateFirstUser: %v", err)
	}
	u, err := userService.GetFirstUser()
	if err != nil {
		t.Fatalf("GetFirstUser: %v", err)
	}
	if u.Username != "alice" {
		t.Errorf("canonical username = %q, want alice", u.Username)
	}
	if u.DisplayUsername() != "Alice" {
		t.Errorf("display username = %q, want Alice", u.DisplayUsername())
	}
	if !crypto.CheckPasswordHash(u.Password, "secret-pw") {
		t.Fatal("password hash does not verify against secret-pw")
	}
	oldHash := u.Password

	// --user WITHOUT --pass path: rename only; the password hash must be UNTOUCHED.
	if err := userService.SetFirstUsername("BoB"); err != nil {
		t.Fatalf("SetFirstUsername: %v", err)
	}
	u2, _ := userService.GetFirstUser()
	if u2.Username != "bob" {
		t.Errorf("canonical username after rename = %q, want bob", u2.Username)
	}
	if u2.DisplayUsername() != "BoB" {
		t.Errorf("display username after rename = %q, want BoB", u2.DisplayUsername())
	}
	if u2.Password != oldHash {
		t.Error("SetFirstUsername changed the password hash — it must preserve it")
	}
	if !crypto.CheckPasswordHash(u2.Password, "secret-pw") {
		t.Error("password no longer verifies after a username-only change")
	}

	// SetFirstUsername rejects an empty username.
	if err := userService.SetFirstUsername(""); err == nil {
		t.Error("SetFirstUsername(\"\") should error")
	}
}
