package server

import (
	"context"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bornholm/genai/llm"
	tezcatlv1 "github.com/bornholm/tezcatl/gen/tezcatl/v1"
	itzclient "github.com/bornholm/tezcatl/internal/itztli/client"
	itzconfig "github.com/bornholm/tezcatl/internal/itztli/config"
	"github.com/bornholm/tezcatl/internal/itztli/explain"
	"google.golang.org/grpc"
)

// slowModel answers only once released, standing in for a model that
// takes longer than a reverse proxy's patience.
type slowModel struct {
	release chan struct{}
	failure error

	mutex sync.Mutex
	count int
}

func (m *slowModel) calls() int {
	m.mutex.Lock()
	defer m.mutex.Unlock()

	return m.count
}

func (m *slowModel) ChatCompletion(ctx context.Context, funcs ...llm.ChatCompletionOptionFunc) (llm.ChatCompletionResponse, error) {
	m.mutex.Lock()
	m.count++
	m.mutex.Unlock()

	select {
	case <-m.release:
	case <-ctx.Done():
		return nil, ctx.Err()
	}

	if m.failure != nil {
		return nil, m.failure
	}

	return llm.NewChatCompletionResponse(
		llm.NewMessage(llm.RoleAssistant, "Le motif inédit est le déclencheur."),
		llm.NewChatCompletionUsage(0, 0, 0),
	), nil
}

func startExplainFixture(t *testing.T, model *slowModel) (*httptest.Server, *http.Client) {
	t.Helper()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}

	grpcServer := grpc.NewServer()
	tezcatlv1.RegisterAdminServiceServer(grpcServer, &fakeAdmin{})
	go grpcServer.Serve(listener)
	t.Cleanup(grpcServer.Stop)

	cfg := itzconfig.Default()
	cfg.Tezcatl.Target = "tcp://" + listener.Addr().String()
	cfg.Auth.Password.Password = "sesame"

	admin, err := itzclient.New(itzclient.Options{
		Target:   cfg.Tezcatl.Target,
		Window:   cfg.Incidents.Window.AsDuration(),
		CacheTTL: cfg.Incidents.CacheTTL.AsDuration(),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { admin.Close() })

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	server := New(cfg, admin, explain.NewWithClient(model, "fake-model"), "test", logger)

	web := httptest.NewServer(server.Handler())
	t.Cleanup(web.Close)

	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}

	browser := &http.Client{Jar: jar}

	res, err := browser.PostForm(web.URL+"/login", url.Values{"password": {"sesame"}})
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()

	return web, browser
}

// pollUntil follows the pending panel's own poll until it reports a
// final state, or gives up.
func pollUntil(t *testing.T, browser *http.Client, url string) string {
	t.Helper()

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		status, body := get(t, browser, url)
		if status != http.StatusOK {
			t.Fatalf("poll = %d", status)
		}

		if !strings.Contains(body, "réponse en cours") {
			return body
		}

		time.Sleep(20 * time.Millisecond)
	}

	t.Fatal("the explanation never came back")

	return ""
}

// TestExplainOutlivesTheRequestThatStartedIt is the regression that
// broke behind Dokku's nginx: a model slower than the proxy's 60s
// timeout used to kill the generation with the request. The POST must
// answer at once, and the work must go on.
func TestExplainOutlivesTheRequestThatStartedIt(t *testing.T) {
	model := &slowModel{release: make(chan struct{})}
	web, browser := startExplainFixture(t, model)

	explainURL := web.URL + "/incidents/ev-trigger/explain"

	started := time.Now()

	res, err := browser.Post(explainURL, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(res.Body)
	res.Body.Close()

	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Fatalf("the POST waited %s for the model; it must answer at once", elapsed)
	}

	if !strings.Contains(string(body), "réponse en cours") {
		t.Fatalf("the POST must answer with the pending panel: %s", body)
	}

	// The pending panel polls, and hides the header button meanwhile.
	if !strings.Contains(string(body), "/explain/status") ||
		!strings.Contains(string(body), `id="explain-btn-slot" hx-swap-oob="true"></div>`) {
		t.Errorf("pending panel misses its poll or the out-of-band button swap: %s", body)
	}

	// The model answers long after the POST is done and gone.
	close(model.release)

	final := pollUntil(t, browser, web.URL+"/incidents/ev-trigger/explain/status")

	if !strings.Contains(final, "Le motif inédit est le déclencheur.") {
		t.Fatalf("the answer never reached the panel: %s", final)
	}

	if !strings.Contains(final, "Elle peut se tromper") || !strings.Contains(final, "Régénérer") {
		t.Errorf("the final panel misses its disclaimer or actions: %s", final)
	}

	// Reloading the page finds the answer instead of paying for it
	// again.
	_, page := get(t, browser, web.URL+"/incidents/ev-trigger")
	if !strings.Contains(page, "Le motif inédit est le déclencheur.") {
		t.Error("a reload must show the explanation already generated")
	}

	// Dismissing forgets it and brings the button back.
	_, reset := get(t, browser, web.URL+"/incidents/ev-trigger/explain/reset")
	if !strings.Contains(reset, ">Explain<") || strings.Contains(reset, "Le motif inédit") {
		t.Errorf("dismissing must clear the panel and restore the button: %s", reset)
	}
}

// TestExplainDoesNotDoubleGenerate guards the cost: a second click
// while the model is answering joins the generation in flight.
func TestExplainDoesNotDoubleGenerate(t *testing.T) {
	model := &slowModel{release: make(chan struct{})}
	web, browser := startExplainFixture(t, model)

	explainURL := web.URL + "/incidents/ev-trigger/explain"

	for range 3 {
		res, err := browser.Post(explainURL, "", nil)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
	}

	close(model.release)

	pollUntil(t, browser, web.URL+"/incidents/ev-trigger/explain/status")

	if calls := model.calls(); calls != 1 {
		t.Fatalf("the model was called %d times, want 1", calls)
	}
}
