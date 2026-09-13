package httptestutil

import (
	"net/http"
	"testing"
)

func TestWithHostPreservesRequestTargetAndExplicitHost(t *testing.T) {
	r, err := http.NewRequest("GET", "/private%252Ffile?key=value", nil)
	if err != nil {
		t.Fatal(err)
	}
	got := WithHost(r)
	if got.Host != "localhost" || r.Host != "" || got.URL.String() != r.URL.String() {
		t.Fatalf("request changed: %+v", got)
	}
	r.Host = "custom.example"
	if WithHost(r) != r || WithHost(r).Host != "custom.example" {
		t.Fatal("explicit host was overwritten")
	}
}
