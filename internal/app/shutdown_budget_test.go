package app

import (
	"context"
	"net"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"sync"
	"testing"
	"time"

	"golang.org/x/time/rate"
	"gopkg.in/yaml.v3"

	"github.com/aryasoni98/alertkube/internal/alert"
	"github.com/aryasoni98/alertkube/internal/silence"
	"github.com/aryasoni98/alertkube/internal/sinks"
	"github.com/aryasoni98/alertkube/internal/watchers"
)

// chartGracePeriod reads terminationGracePeriodSeconds from the chart's
// values.yaml and checks the deployment template falls back to the same
// number, so the budget below is pinned to what the chart actually ships.
func chartGracePeriod(t *testing.T) time.Duration {
	t.Helper()
	raw, err := os.ReadFile("../../helm/values.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var values struct {
		TerminationGracePeriodSeconds int `yaml:"terminationGracePeriodSeconds"`
	}
	if err := yaml.Unmarshal(raw, &values); err != nil {
		t.Fatal(err)
	}
	if values.TerminationGracePeriodSeconds <= 0 {
		t.Fatal("helm/values.yaml sets no terminationGracePeriodSeconds")
	}
	tmpl, err := os.ReadFile("../../helm/templates/deployment.yaml")
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile(`terminationGracePeriodSeconds \| default (\d+)`).FindSubmatch(tmpl)
	if m == nil {
		t.Fatal("deployment template has no terminationGracePeriodSeconds default")
	}
	if fallback, _ := strconv.Atoi(string(m[1])); fallback != values.TerminationGracePeriodSeconds {
		t.Fatalf("deployment template defaults terminationGracePeriodSeconds to %d, values.yaml to %d", fallback, values.TerminationGracePeriodSeconds)
	}
	return time.Duration(values.TerminationGracePeriodSeconds) * time.Second
}

// TestShutdownBudgetFitsGracePeriod pins the shutdown arithmetic described at
// finalSaveTimeout in pipeline.go. Change one timeout, and this says which of
// the others has to move with it.
func TestShutdownBudgetFitsGracePeriod(t *testing.T) {
	grace := chartGracePeriod(t)
	tests := []struct {
		name     string
		got, max time.Duration
	}{
		// After the drain budget the process still shuts down the HTTP
		// servers and flushes traces; all of it must land before SIGKILL.
		{"drain budget + HTTP shutdown + trace flush fit the chart grace period", controllerDrainBudget + httpShutdownTimeout + traceFlushTimeout, grace},
		// A send already in flight when the enrichment drain ends still gets
		// its full dispatchTimeout before the drain deadline, and the final
		// save still fits after that.
		{"enrichment drain + one send + final save fit the drain budget", enrichDrainTimeout + dispatchTimeout + finalSaveTimeout, controllerDrainBudget},
		// dispatchDrainTimeout must not cut a send that has just started.
		{"one send fits the dispatch drain", dispatchTimeout, dispatchDrainTimeout},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.got > tt.max {
				t.Fatalf("%s > %s", tt.got, tt.max)
			}
		})
	}
}

// recordingStore is a persist.Store that keeps the last saved snapshot and
// the context it was saved under.
type recordingStore struct {
	mu      sync.Mutex
	snap    *alert.Snapshot
	saveCtx context.Context
}

func (s *recordingStore) Load(context.Context) (*alert.Snapshot, error) { return nil, nil }

func (s *recordingStore) Save(ctx context.Context, snap *alert.Snapshot) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.snap, s.saveCtx = snap, ctx
	return nil
}

func (s *recordingStore) last() (*alert.Snapshot, context.Context) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.snap, s.saveCtx
}

// A sink that never answers, a full queue, and an informer handler parked in
// enqueue behind it. Before, close(stop) only happened inside disp.Shutdown,
// after stopInformers and wg.Wait, so the parked handler held stopInformers
// until a worker freed a slot, and nothing bounded the drain. Shutdown must
// release the producer, stop at the drain deadline, and still save every
// undelivered job in the outbox so the next leader replays it.
func TestShutdownWithBlockedSinkFitsBudgetAndSavesOutbox(t *testing.T) {
	prev := controllerDrainBudget
	controllerDrainBudget = finalSaveTimeout + 200*time.Millisecond
	t.Cleanup(func() { controllerDrainBudget = prev })

	started, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	reg := sinks.NewRegistry()
	reg.Add(&testSink{name: "a", fn: func(*alert.Alert) error {
		once.Do(func() { close(started) })
		<-release
		return nil
	}})
	reg.SetRate("a", rate.Limit(100000), 100000)
	disp := newDispatcher(reg, 1, 1) // one worker, one queue slot
	disp.Start()
	t.Cleanup(func() { close(release) })

	route := []string{"a"}
	disp.enqueue(alert.New(alert.KindPod, "ns", "in-flight", "X", alert.SeverityCritical), route, nil)
	select {
	case <-started:
	case <-timeoutAfter():
		t.Fatal("delivery did not start")
	}
	disp.enqueue(alert.New(alert.KindPod, "ns", "queued", "X", alert.SeverityCritical), route, nil)
	producerDone := make(chan struct{})
	go func() {
		defer close(producerDone)
		disp.enqueue(alert.New(alert.KindPod, "ns", "parked", "X", alert.SeverityCritical), route, nil)
	}()
	waitForPending(t, disp, 3) // the parked job is in the outbox before it blocks in submit

	saver := &recordingStore{}
	var wg sync.WaitGroup
	// stopInformers stands in for factory.Shutdown, which joins the informer
	// handler: here, the producer parked in submit.
	stopInformers := func() { <-producerDone }
	begin := time.Now()
	returned := make(chan struct{})
	go func() {
		defer close(returned)
		shutdown(nil, stopInformers, func() {}, &wg, disp, saver, alert.NewStore(time.Minute, time.Minute, nil), silence.NewStore())
	}()
	select {
	case <-returned:
	case <-time.After(controllerDrainBudget + time.Second):
		t.Fatal("shutdown did not return within the drain budget: the producer parked in submit still blocks stopInformers")
	}
	if elapsed := time.Since(begin); elapsed > controllerDrainBudget {
		t.Fatalf("shutdown took %s, budget %s", elapsed, controllerDrainBudget)
	}

	snap, saveCtx := saver.last()
	if snap == nil {
		t.Fatal("shutdown skipped the final save")
	}
	if dl, ok := saveCtx.Deadline(); !ok || time.Until(dl) < finalSaveTimeout/2 {
		t.Fatalf("final save must run on a fresh finalSaveTimeout context, got deadline %v (set %v)", dl, ok)
	}
	got := map[string]bool{}
	for _, rec := range snap.Pending {
		got[rec.Alert.Name] = true
	}
	for _, name := range []string{"in-flight", "queued", "parked"} {
		if !got[name] {
			t.Errorf("final save is missing the undelivered %q job: %+v", name, snap.Pending)
		}
	}
}

// blockingDrainer is a watcher whose Drain waits for its context.
type blockingDrainer struct{ watchers.Watcher }

func (blockingDrainer) Drain(ctx context.Context) { <-ctx.Done() }

// drainWatchers must stop at the shutdown deadline it is given, not only at
// its own enrichDrainTimeout cap.
func TestDrainWatchersStopsAtShutdownDeadline(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	returned := make(chan struct{})
	go func() {
		drainWatchers(ctx, []watchers.Watcher{blockingDrainer{}})
		close(returned)
	}()
	select {
	case <-returned:
	case <-timeoutAfter():
		t.Fatal("drainWatchers ignored the shutdown deadline")
	}
}

// serveOn starts srv on a loopback listener and returns its URL and a channel
// closed once Serve returns (Shutdown closes the listener first).
func serveOn(t *testing.T, srv *http.Server) (string, <-chan struct{}) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	served := make(chan struct{})
	go func() {
		defer close(served)
		_ = srv.Serve(ln)
	}()
	return "http://" + ln.Addr().String(), served
}

// The metrics and API servers shut down under one httpShutdownTimeout, not
// one each: a server holding a slow request must not delay the others, or two
// servers spend twice the slice the budget test reserves.
func TestShutdownServersShareOneDeadline(t *testing.T) {
	inFlight, release := make(chan struct{}), make(chan struct{})
	releaseOnce := sync.OnceFunc(func() { close(release) })
	t.Cleanup(releaseOnce)
	busy := &http.Server{ReadHeaderTimeout: time.Second, Handler: http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		close(inFlight)
		<-release
	})}
	idle := &http.Server{ReadHeaderTimeout: time.Second, Handler: http.NotFoundHandler()}
	busyURL, _ := serveOn(t, busy)
	_, idleServed := serveOn(t, idle)
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, busyURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		if resp, err := http.DefaultClient.Do(req); err == nil {
			resp.Body.Close()
		}
	}()
	select {
	case <-inFlight:
	case <-timeoutAfter():
		t.Fatal("request did not reach the busy server")
	}

	done := make(chan struct{})
	go func() {
		shutdownServers([]*http.Server{busy, idle})
		close(done)
	}()
	select {
	case <-idleServed:
	case <-timeoutAfter():
		t.Fatal("the idle server waited for the busy server's shutdown")
	}
	releaseOnce()
	select {
	case <-done:
	case <-time.After(httpShutdownTimeout + time.Second):
		t.Fatal("shutdownServers did not return")
	}
}
