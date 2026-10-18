package sites

import (
	"testing"
)

func TestSite(t *testing.T) {
	t.Parallel()

	n, err := Parse("home = 192.0.2.0/24 , shed=192.0.2.128/25, cabin=198.51.100.0/24,cabin=203.0.113.9, lab=2001:db8::/32,lab=2001:db8::/32,")
	if err != nil {
		t.Fatal(err)
	}

	for addr, want := range map[string]string{
		"192.0.2.1":          "home",
		"192.0.2.127":        "home",
		"192.0.2.128":        "shed",
		"198.51.100.255":     "cabin",
		"203.0.113.9":        "cabin",
		"203.0.113.10":       "",
		"::ffff:192.0.2.1":   "home",
		"[2001:db8::1]":      "lab",
		"2001:db8::1":        "lab",
		"[fe80::1%eth0]":     "",
		"[2001:db8::1%eth0]": "lab",
		"10.0.0.1":           "",
		"":                   "",
		"192.0.2.1:514":      "",
	} {
		if got := n.Site(addr); got != want {
			t.Errorf("Site(%q) = %q, want %q", addr, got, want)
		}
	}
}

// A network written with host bits is the network they are in.
func TestParseMasks(t *testing.T) {
	t.Parallel()

	n, err := Parse("home=192.0.2.77/24")
	if err != nil {
		t.Fatal(err)
	}
	if got := n.Site("192.0.2.1"); got != "home" {
		t.Errorf("Site = %q, want home: 192.0.2.77/24 is 192.0.2.0/24", got)
	}
}

func TestParseNothing(t *testing.T) {
	t.Parallel()

	for _, s := range []string{"", " ", ",,"} {
		n, err := Parse(s)
		if err != nil || n.Site("192.0.2.1") != "" {
			t.Errorf("Parse(%q) = %v, %v: want no sites", s, n, err)
		}
	}

	var none Networks
	if got := none.Site("192.0.2.1"); got != "" {
		t.Errorf("no sites name %q", got)
	}
}

func TestParseRefuses(t *testing.T) {
	t.Parallel()

	for _, s := range []string{"home", "home=", "=192.0.2.0/24", "home=192.0.2.0/33", "home=192.0.2", "home=nowhere", "home=192.0.2.0/24,cabin=192.0.2.0/24", "home=192.0.2.0/24,cabin=192.0.2.9/24"} {
		if _, err := Parse(s); err == nil {
			t.Errorf("Parse(%q) gave no error", s)
		}
	}
}
