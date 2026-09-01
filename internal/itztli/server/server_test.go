package server

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	tezcatlv1 "github.com/bornholm/tezcatl/gen/tezcatl/v1"
	"github.com/bornholm/tezcatl/internal/core/model"
	itzclient "github.com/bornholm/tezcatl/internal/itztli/client"
	itzconfig "github.com/bornholm/tezcatl/internal/itztli/config"
	"google.golang.org/grpc"

	"net/http/httptest"
)

// fakeAdmin is a canned AdminService: enough for the UI to have
// something true to show.
type fakeAdmin struct {
	tezcatlv1.UnimplementedAdminServiceServer

	mutex           sync.Mutex
	markedTemplates map[string]string
	markedMetrics   map[string]bool
}

func (f *fakeAdmin) ListEvents(ctx context.Context, req *tezcatlv1.ListEventsRequest) (*tezcatlv1.ListEventsResponse, error) {
	now := time.Now()

	events := []model.Event{
		{
			ID:          "ev-trigger",
			Kind:        "anomaly.log.missing_template",
			Source:      "production/automata",
			Service:     "automata",
			Environment: "production",
			Timestamp:   now.Add(-2 * time.Hour),
			Severity:    model.SeverityCritical,
			Confidence:  0.99,
			Summary:     "expected log template not seen for 1h 40m (mean interval 27m 48s): HTTP server listening on <IP>:<NUM>",
			Signals: []model.Signal{{
				Kind:      "log.missing_template",
				Modality:  model.ModalityLog,
				Source:    "production/automata",
				Timestamp: now.Add(-2 * time.Hour),
				Score:     0.99,
				Summary:   "expected log template not seen for 1h 40m (mean interval 27m 48s): HTTP server listening on <IP>:<NUM>",
			}},
			RelatedChanges: []model.RelatedChange{{
				Source:        "production/automata",
				Change:        model.ChangeRecord{Type: "deployment", Version: "automata:1.4.2"},
				Timestamp:     now.Add(-2*time.Hour - 20*time.Second),
				OffsetSeconds: -20,
			}},
			Context: model.Context{
				Before: []model.Observation{{
					Modality:  model.ModalityLog,
					Timestamp: now.Add(-2*time.Hour - time.Minute),
					Log:       &model.LogRecord{Raw: "INFO worker pool drained, 0 tasks pending"},
				}},
			},
		},
		{
			ID:         "ev-second",
			Kind:       "anomaly.log.new_template",
			Source:     "production/automata",
			Service:    "automata",
			Timestamp:  now.Add(-110 * time.Minute),
			Severity:   model.SeverityWarning,
			Confidence: 0.71,
			Summary:    "new log template after learning period: ERROR lost connection",
		},
		{
			ID:        "ev-info",
			Kind:      "pipeline.stats",
			Source:    "tezcatl",
			Timestamp: now.Add(-1 * time.Hour),
			Severity:  model.SeverityInfo,
			Summary:   "not an anomaly, must not appear",
		},
	}

	res := &tezcatlv1.ListEventsResponse{}
	for _, event := range events {
		encoded, err := json.Marshal(event)
		if err != nil {
			return nil, err
		}

		res.Events = append(res.Events, &tezcatlv1.EventEnvelope{Json: string(encoded)})
	}

	return res, nil
}

func (f *fakeAdmin) ListTemplates(ctx context.Context, req *tezcatlv1.ListTemplatesRequest) (*tezcatlv1.ListTemplatesResponse, error) {
	return &tezcatlv1.ListTemplatesResponse{
		Templates: []*tezcatlv1.TemplateInfo{
			{Partition: "production/automata", Template: "HTTP server listening on <IP>:<NUM>", Size: 6841, Marking: "normal"},
			{Partition: "production/ssh", Template: "Connection closed by <IP> port <NUM> [preauth]", Size: 608},
		},
	}, nil
}

func (f *fakeAdmin) ListMetrics(ctx context.Context, req *tezcatlv1.ListMetricsRequest) (*tezcatlv1.ListMetricsResponse, error) {
	f.mutex.Lock()
	defer f.mutex.Unlock()

	return &tezcatlv1.ListMetricsResponse{
		Metrics: []*tezcatlv1.MetricInfo{
			{Key: "production/host/system.cpu.percent", Samples: 2840, Mean: 1.67, StdDev: 0.42, Recent: 16.94, Ignored: f.markedMetrics["production/host/system.cpu.percent"]},
			{Key: "demo/boutique/http_latency_p95", Samples: 14, Mean: 0.31, StdDev: 0.04, Recent: 0.33, Warmup: true},
		},
	}, nil
}

func (f *fakeAdmin) MarkTemplate(ctx context.Context, req *tezcatlv1.MarkTemplateRequest) (*tezcatlv1.MarkTemplateResponse, error) {
	f.mutex.Lock()
	defer f.mutex.Unlock()

	if f.markedTemplates == nil {
		f.markedTemplates = map[string]string{}
	}
	f.markedTemplates[req.GetTemplate()] = req.GetMarking()

	return &tezcatlv1.MarkTemplateResponse{}, nil
}

func (f *fakeAdmin) MarkMetric(ctx context.Context, req *tezcatlv1.MarkMetricRequest) (*tezcatlv1.MarkMetricResponse, error) {
	f.mutex.Lock()
	defer f.mutex.Unlock()

	if f.markedMetrics == nil {
		f.markedMetrics = map[string]bool{}
	}
	f.markedMetrics[req.GetPattern()] = req.GetIgnore()

	return &tezcatlv1.MarkMetricResponse{}, nil
}

func startFixture(t *testing.T) (*fakeAdmin, *httptest.Server, *http.Client) {
	t.Helper()

	fake := &fakeAdmin{}

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}

	grpcServer := grpc.NewServer()
	tezcatlv1.RegisterAdminServiceServer(grpcServer, fake)
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
	web := httptest.NewServer(New(cfg, admin, nil, "test", logger).Handler())
	t.Cleanup(web.Close)

	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}

	return fake, web, &http.Client{Jar: jar}
}

func login(t *testing.T, web *httptest.Server, browser *http.Client) {
	t.Helper()

	res, err := browser.PostForm(web.URL+"/login", url.Values{"password": {"sesame"}})
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()

	if res.Request.URL.Path != "/" {
		t.Fatalf("login should land on /, got %s", res.Request.URL.Path)
	}
}

func get(t *testing.T, browser *http.Client, url string) (int, string) {
	t.Helper()

	res, err := browser.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()

	body, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}

	return res.StatusCode, string(body)
}

func TestAuthGate(t *testing.T) {
	_, web, browser := startFixture(t)

	status, body := get(t, browser, web.URL+"/")
	if status != http.StatusOK || !strings.Contains(body, "Mot de passe") {
		t.Fatalf("unauthenticated / should land on the login page, got %d", status)
	}

	res, err := browser.PostForm(web.URL+"/login", url.Values{"password": {"wrong"}})
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()

	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("wrong password should get 401, got %d", res.StatusCode)
	}

	if status, _ := get(t, browser, web.URL+"/healthz"); status != http.StatusOK {
		t.Fatalf("healthz should not need auth, got %d", status)
	}
}

func TestIncidentPages(t *testing.T) {
	_, web, browser := startFixture(t)
	login(t, web, browser)

	status, body := get(t, browser, web.URL+"/")
	if status != http.StatusOK {
		t.Fatalf("GET / = %d", status)
	}

	for _, expected := range []string{
		"Derniers incidents",
		"automata",
		// The two anomalies group into one incident; the pipeline event
		// must not appear.
		"missing_template",
		"deployment corrélé",
	} {
		if !strings.Contains(body, expected) {
			t.Errorf("home page misses %q", expected)
		}
	}

	if strings.Contains(body, "not an anomaly") {
		t.Error("a non-anomaly event leaked into the incident list")
	}

	status, body = get(t, browser, web.URL+"/incidents/ev-trigger")
	if status != http.StatusOK {
		t.Fatalf("GET /incidents/ev-trigger = %d", status)
	}

	for _, expected := range []string{
		"Déclencheur",
		"Changements corrélés",
		"automata:1.4.2",
		"Preuves",
		"worker pool drained",
		"Événements sous-jacents",
	} {
		if !strings.Contains(body, expected) {
			t.Errorf("detail page misses %q", expected)
		}
	}

	// No genai configured: no Explain button.
	if strings.Contains(body, ">Explain<") {
		t.Error("the Explain button must not exist without a genai provider")
	}
}

func TestTemplatesAndMarking(t *testing.T) {
	fake, web, browser := startFixture(t)
	login(t, web, browser)

	status, body := get(t, browser, web.URL+"/templates")
	if status != http.StatusOK || !strings.Contains(body, "HTTP server listening on") {
		t.Fatalf("GET /templates = %d, body misses the template", status)
	}

	res, err := browser.PostForm(web.URL+"/templates/mark", url.Values{
		"template":  {"Connection closed by <IP> port <NUM> [preauth]"},
		"partition": {"production/ssh"},
		"size":      {"608"},
		"marking":   {"ignore"},
	})
	if err != nil {
		t.Fatal(err)
	}
	row, _ := io.ReadAll(res.Body)
	res.Body.Close()

	if res.StatusCode != http.StatusOK {
		t.Fatalf("mark template = %d", res.StatusCode)
	}

	if got := fake.markedTemplates["Connection closed by <IP> port <NUM> [preauth]"]; got != "ignore" {
		t.Fatalf("marking did not reach the server, got %q", got)
	}

	if !strings.Contains(string(row), "ignore") || !strings.Contains(string(row), "608") {
		t.Errorf("the returned row should show the new marking: %s", row)
	}

	if status, body := get(t, browser, web.URL+"/templates/rows?q=preauth"); status != http.StatusOK ||
		!strings.Contains(body, "preauth") || strings.Contains(body, "HTTP server listening") {
		t.Errorf("filtering on q=preauth failed (%d)", status)
	}
}

func TestMetricsAndMarking(t *testing.T) {
	fake, web, browser := startFixture(t)
	login(t, web, browser)

	status, body := get(t, browser, web.URL+"/metrics")
	if status != http.StatusOK || !strings.Contains(body, "system.cpu.percent") {
		t.Fatalf("GET /metrics = %d", status)
	}

	if !strings.Contains(body, "apprentissage") {
		t.Error("the warmup series should be labeled en apprentissage")
	}

	res, err := browser.PostForm(web.URL+"/metrics/mark", url.Values{
		"pattern": {"production/host/system.cpu.percent"},
		"ignore":  {"true"},
	})
	if err != nil {
		t.Fatal(err)
	}
	row, _ := io.ReadAll(res.Body)
	res.Body.Close()

	if res.StatusCode != http.StatusOK {
		t.Fatalf("mark metric = %d", res.StatusCode)
	}

	if !fake.markedMetrics["production/host/system.cpu.percent"] {
		t.Fatal("the ignore marking did not reach the server")
	}

	if !strings.Contains(string(row), "cesser d") {
		t.Errorf("the returned row should offer to unignore: %s", row)
	}
}
