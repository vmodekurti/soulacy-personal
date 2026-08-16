// fairshare_test.go — MU-024 criterion 4. Every assertion is on the SHARE
// held, never on timing: a fairness test that races is a fairness test nobody
// believes when it fails.
package quota

import (
	"context"
	"sync"
	"testing"
	"time"
)

func acquire(t *testing.T, f *FairShare, workspaceID string) func() {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	release, err := f.Acquire(ctx, workspaceID)
	if err != nil {
		t.Fatalf("%s could not acquire: %v", workspaceID, err)
	}
	return release
}

// One tenant alone gets the whole capacity: fairness must not idle slots
// nobody else wants.
func TestASingleWorkspaceGetsTheWholeCapacity(t *testing.T) {
	f := NewFairShare(4)
	for i := 0; i < 4; i++ {
		acquire(t, f, "ws-a")
	}
	if got := f.Held()["ws-a"]; got != 4 {
		t.Fatalf("held = %d, want the full capacity of 4", got)
	}
}

// The property the criterion names. A tenant that submits a hundred runs must
// not take every slot and hold them while another tenant waits — without
// exceeding any budget, because it is not spending faster than allowed, only
// first.
func TestAFloodingWorkspaceCannotStarveAnother(t *testing.T) {
	f := NewFairShare(4)
	// ws-a floods, taking everything it can while alone.
	var releases []func()
	for i := 0; i < 4; i++ {
		releases = append(releases, acquire(t, f, "ws-a"))
	}

	// ws-b arrives and queues.
	arrived := make(chan struct{})
	admitted := make(chan struct{})
	go func() {
		close(arrived)
		release, err := f.Acquire(context.Background(), "ws-b")
		if err != nil {
			return
		}
		close(admitted)
		_ = release
	}()
	<-arrived

	// One slot comes free. It must go to the waiting tenant, not back to the
	// incumbent — first-come would hand it straight back to ws-a.
	releases[0]()

	select {
	case <-admitted:
	case <-time.After(2 * time.Second):
		t.Fatal("the released slot never reached the waiting workspace")
	}
	held := f.Held()
	if held["ws-b"] != 1 {
		t.Fatalf("ws-b holds %d after a slot was freed", held["ws-b"])
	}
	if held["ws-a"] != 3 {
		t.Fatalf("ws-a holds %d, want 3", held["ws-a"])
	}
}

// While a competitor is waiting, the incumbent is capped at its share rather
// than being allowed to re-take every slot it releases.
func TestAnIncumbentCannotReclaimPastItsShareWhileAnotherWaits(t *testing.T) {
	f := NewFairShare(4)
	var releases []func()
	for i := 0; i < 4; i++ {
		releases = append(releases, acquire(t, f, "ws-a"))
	}

	// ws-b queues for two slots.
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := f.Acquire(context.Background(), "ws-b"); err != nil {
				t.Error(err)
			}
		}()
	}
	// Give the waiters time to enqueue. Not a timing assertion — the
	// assertion below is on the share; this only ensures the queue is real.
	waitUntil(t, func() bool { return len(f.Held()) > 0 })
	time.Sleep(50 * time.Millisecond)

	releases[0]()
	releases[1]()
	wg.Wait()

	held := f.Held()
	if held["ws-a"] != 2 || held["ws-b"] != 2 {
		t.Fatalf("held = %v, want an even 2/2 split of 4 slots between two tenants", held)
	}
}

// With capacity 4 and three competitors, the share is ceil(4/3)=2, not
// floor(4/3)=1 — fairness that leaves a slot permanently idle while three
// tenants queue for it is not the goal.
func TestTheShareRoundsUpSoCapacityIsNotWasted(t *testing.T) {
	f := NewFairShare(4)
	acquire(t, f, "ws-a")
	acquire(t, f, "ws-b")
	acquire(t, f, "ws-c")
	// A fourth slot exists and somebody should be able to use it.
	acquire(t, f, "ws-a")
	total := 0
	for _, n := range f.Held() {
		total += n
	}
	if total != 4 {
		t.Fatalf("only %d of 4 slots are in use; capacity was wasted in the name of fairness", total)
	}
}

// A waiter that gives up must not leave a slot promised to a channel nobody
// reads — that slot would be lost for the life of the process.
func TestAWaiterThatGivesUpDoesNotLeakItsSlot(t *testing.T) {
	f := NewFairShare(1)
	release := acquire(t, f, "ws-a")

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := f.Acquire(ctx, "ws-b"); err == nil {
		t.Fatal("the waiter was admitted despite no free slot")
	}
	release()

	// The single slot must still be usable.
	done := make(chan struct{})
	go func() {
		defer close(done)
		if _, err := f.Acquire(context.Background(), "ws-c"); err != nil {
			t.Error(err)
		}
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("the capacity was leaked by an abandoned waiter")
	}
}

// Releasing twice must not corrupt the accounting: a caller that defers
// release and also releases on an error path is ordinary.
func TestReleasingTwiceIsHarmless(t *testing.T) {
	f := NewFairShare(2)
	release := acquire(t, f, "ws-a")
	release()
	release()
	if got := f.Held()["ws-a"]; got != 0 {
		t.Fatalf("held = %d after a double release", got)
	}
	// And the capacity is intact rather than inflated.
	acquire(t, f, "ws-b")
	acquire(t, f, "ws-c")
	total := 0
	for _, n := range f.Held() {
		total += n
	}
	if total != 2 {
		t.Fatalf("total held = %d, want 2 — a double release inflated capacity", total)
	}
}

// Unlimited capacity is the single-tenant answer and must not queue.
func TestZeroCapacityMeansUnlimited(t *testing.T) {
	f := NewFairShare(0)
	for i := 0; i < 100; i++ {
		acquire(t, f, "ws-a")
	}
	var nilShare *FairShare
	if _, err := nilShare.Acquire(context.Background(), "ws-a"); err != nil {
		t.Fatalf("a nil scheduler refused: %v", err)
	}
}

// Under heavy contention nobody is starved and the capacity is never
// exceeded.
func TestUnderContentionNobodyIsStarvedAndCapacityHolds(t *testing.T) {
	const capacity = 3
	f := NewFairShare(capacity)
	workspaces := []string{"ws-a", "ws-b", "ws-c", "ws-d"}

	var mu sync.Mutex
	completed := map[string]int{}
	var wg sync.WaitGroup
	for _, ws := range workspaces {
		for i := 0; i < 10; i++ {
			wg.Add(1)
			go func(ws string) {
				defer wg.Done()
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancel()
				release, err := f.Acquire(ctx, ws)
				if err != nil {
					return
				}
				mu.Lock()
				held := 0
				for _, n := range f.Held() {
					held += n
				}
				if held > capacity {
					t.Errorf("%d slots held, capacity is %d", held, capacity)
				}
				completed[ws]++
				mu.Unlock()
				release()
			}(ws)
		}
	}
	wg.Wait()

	for _, ws := range workspaces {
		if completed[ws] != 10 {
			t.Fatalf("%s completed %d of 10 — it was starved", ws, completed[ws])
		}
	}
}

func waitUntil(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.After(2 * time.Second)
	for !cond() {
		select {
		case <-deadline:
			t.Fatal("condition never held")
		default:
			time.Sleep(time.Millisecond)
		}
	}
}

// Promotion rotates across WORKSPACES. Draining one workspace's whole queue
// before looking at the next is first-come again, wearing a queue.
func TestPromotionRotatesAcrossWorkspacesRatherThanDrainingOne(t *testing.T) {
	f := NewFairShare(1)
	holder := acquire(t, f, "ws-holder")

	// ws-b queues two waiters, then ws-c queues one. If promotion drained
	// ws-b's queue, ws-c would go last despite arriving before ws-b's second
	// slot was ever available.
	// Enqueued one at a time: three goroutines racing to queue would make the
	// arrival order nondeterministic, and the assertion below is about order.
	bFirst := queueWaiter(t, f, "ws-b")
	time.Sleep(30 * time.Millisecond)
	bSecond := queueWaiter(t, f, "ws-b")
	time.Sleep(30 * time.Millisecond)
	cOnly := queueWaiter(t, f, "ws-c")
	time.Sleep(30 * time.Millisecond)

	holder()
	release := awaitOne(t, bFirst, "ws-b's first waiter")

	release()
	// The next slot must go to ws-c, not to ws-b's second waiter.
	select {
	case r := <-cOnly:
		r()
	case r := <-bSecond:
		r()
		t.Fatal("ws-b took two consecutive slots while ws-c waited — promotion drained one workspace")
	case <-time.After(2 * time.Second):
		t.Fatal("no waiter was promoted")
	}
}

func queueWaiter(t *testing.T, f *FairShare, workspaceID string) chan func() {
	t.Helper()
	admitted := make(chan func(), 1)
	go func() {
		release, err := f.Acquire(context.Background(), workspaceID)
		if err != nil {
			return
		}
		admitted <- release
	}()
	return admitted
}

func awaitOne(t *testing.T, ch chan func(), what string) func() {
	t.Helper()
	select {
	case release := <-ch:
		return release
	case <-time.After(2 * time.Second):
		t.Fatalf("%s was never admitted", what)
		return nil
	}
}
