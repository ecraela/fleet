package localauth

import "testing"

func TestPasswordFormatAndBounds(t *testing.T) {
	hash, err := Hash("a long synthetic password")
	if err != nil || !ValidHash(hash) || !Verify(hash, "a long synthetic password") {
		t.Fatal("valid password rejected")
	}
	if Verify(hash, "wrong password") || Verify("$argon2id$v=19$m=999999999,t=99,p=255$bad$hash", "anything") {
		t.Fatal("invalid password/hash accepted")
	}
	for _, p := range []string{"short", string(make([]byte, 1025))} {
		if _, err := Hash(p); err == nil {
			t.Fatal("invalid password accepted")
		}
	}
	if name, err := Username(" ADMIN@Example.Test "); err != nil || name != "admin@example.test" {
		t.Fatal("username normalization")
	}
	for _, name := range []string{"ab", "admin name", "üser"} {
		if _, err := Username(name); err == nil {
			t.Fatal("invalid username")
		}
	}
}
