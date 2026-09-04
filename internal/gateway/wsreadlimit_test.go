package gateway

import (
	"strings"
	"testing"
)

// The WebSocket read loop had no read limit, and the library's default is NO
// limit — ReadMessage is an io.ReadAll underneath. Fiber's BodyLimit does not
// apply to a hijacked connection, and the IP rate limiter deliberately skips
// /ws, so one authenticated client sending a single huge text frame buffered the
// whole thing in RAM.
//
// This asserts the guard is INSTALLED, because that is what the defect was: the
// limit is a property of the connection, set before the loop, and a unit test on
// a helper would say nothing about whether it is called. The read loop discards
// every frame it receives, so there is no behaviour to observe either way.
func TestWSHandler_SetsAReadLimitBeforeTheReadLoop(t *testing.T) {
	src := readGatewaySource(t, "events.go")
	findLine(t, src, "conn.SetReadLimit(maxWSFrameBytes)")

	limitAt := strings.Index(src, "conn.SetReadLimit(")
	loopAt := strings.Index(src, "if _, _, err := conn.ReadMessage(); err != nil {")
	if limitAt < 0 || loopAt < 0 {
		t.Fatal("the read loop or the limit call has moved; this rule now guards nothing")
	}
	if limitAt > loopAt {
		t.Fatal("the read limit is set AFTER the read loop, so the first frame is unbounded")
	}
	if maxWSFrameBytes <= 0 {
		t.Fatalf("maxWSFrameBytes = %d, which disables the limit", maxWSFrameBytes)
	}
}
