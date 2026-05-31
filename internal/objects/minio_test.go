package objects

import "testing"

func TestObjectKeyScopesPathToApp(t *testing.T) {
	key, err := objectKey("hello", "avatars/user.png")
	if err != nil {
		t.Fatal(err)
	}
	if key != "apps/hello/avatars/user.png" {
		t.Fatalf("key = %q", key)
	}
}

func TestObjectKeyRejectsEmptyPath(t *testing.T) {
	if _, err := objectKey("hello", ""); err == nil {
		t.Fatal("expected empty object path to fail")
	}
}
