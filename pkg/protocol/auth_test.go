package protocol

import "testing"

func TestValidName(t *testing.T) {
	for _, ok := range []string{"prod-1", "a", "web.eu_1", "abcdefghijabcdefghijabcdefghij12"} {
		if !ValidName(ok) {
			t.Errorf("ValidName(%q) = false", ok)
		}
	}
	for _, bad := range []string{"", "-x", "relay", "a/b", "has space", "abcdefghijabcdefghijabcdefghij123"} {
		if ValidName(bad) {
			t.Errorf("ValidName(%q) = true", bad)
		}
	}
	if got := SanitizeName("Ricky's MacBook Pro.local"); got != "ricky-s-macbook-pro.local" {
		t.Errorf("SanitizeName = %q", got)
	}
	if NameFromID("org_x/prod-1") != "prod-1" || NameFromID("prod-1") != "prod-1" {
		t.Error("NameFromID")
	}
}
