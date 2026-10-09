package domain

import "testing"

func TestPermissionBoundaries(t *testing.T) {
	for _, c := range []struct {
		role, action string
		want         bool
	}{{"viewer", "read", true}, {"viewer", "manage", false}, {"operator", "operate", true}, {"operator", "manage", false}, {"admin", "manage", true}, {"admin", "ownership", false}, {"owner", "ownership", true}, {"", "read", false}} {
		if got := Can(c.role, c.action); got != c.want {
			t.Fatalf("%s/%s: %v", c.role, c.action, got)
		}
	}
}

func TestPolicyAndTargetsRejectUnsafeInput(t *testing.T) {
	p := DefaultPolicy()
	if e := p.Validate(); e != nil {
		t.Fatal(e)
	}
	p.Timezone = "invalid"
	if p.Validate() == nil {
		t.Fatal("invalid timezone accepted")
	}
	for _, target := range []Target{{Name: "group", Kind: "feishu_group", Credentials: Credentials{Webhook: "http://localhost/admin", Secret: "x"}}, {Name: "mail", Kind: "smtp", Credentials: Credentials{SMTPHost: "smtp.example", SMTPPort: 587, From: "a@example.com", Recipient: "victim@example.com\r\nBcc:other@example.com"}}} {
		if target.Validate() == nil {
			t.Fatal("unsafe channel accepted")
		}
	}
}
