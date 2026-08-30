package main

import "testing"

func TestConnectionCaptureBoundary(t *testing.T) {
	login, domains, err := connectionCaptureBoundary("https://members.hbr.org/login", "hbr.org,members.hbr.org,hbr.org")
	if err != nil {
		t.Fatal(err)
	}
	if login != "https://members.hbr.org/login" || len(domains) != 2 || domains[0] != "hbr.org" || domains[1] != "members.hbr.org" {
		t.Fatalf("login=%q domains=%v", login, domains)
	}
	for _, raw := range []string{"http://hbr.org/login", "https://", "not a url"} {
		if _, _, err := connectionCaptureBoundary(raw, ""); err == nil {
			t.Fatalf("expected %q to fail", raw)
		}
	}
	if _, _, err := connectionCaptureBoundary("https://hbr.org/login", "evil.test"); err == nil {
		t.Fatal("foreign domain boundary must fail")
	}
}

func TestCaptureDomainAllowedDoesNotAcceptLookalikes(t *testing.T) {
	if !captureDomainAllowed(".members.hbr.org", []string{"hbr.org"}) {
		t.Fatal("expected subdomain to be allowed")
	}
	for _, host := range []string{"evilhbr.org", "hbr.org.evil.test"} {
		if captureDomainAllowed(host, []string{"hbr.org"}) {
			t.Fatalf("lookalike %q was allowed", host)
		}
	}
}
