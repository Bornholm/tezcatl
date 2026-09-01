package server

import (
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	tezcatlv1 "github.com/bornholm/tezcatl/gen/tezcatl/v1"
	"github.com/bornholm/tezcatl/internal/core/model"
	itzclient "github.com/bornholm/tezcatl/internal/itztli/client"
	itzconfig "github.com/bornholm/tezcatl/internal/itztli/config"
	"google.golang.org/grpc"
)

// anomaly is a minimal anomaly event, enough to be grouped and
// filtered.
func anomaly(id string, service string, severity model.Severity, at time.Time) model.Event {
	return model.Event{
		ID:        id,
		Kind:      "anomaly.log.new_template",
		Source:    "prod/" + service,
		Service:   service,
		Timestamp: at,
		Severity:  severity,
		Summary:   "new log template after learning period: " + id,
		Signals: []model.Signal{{
			Kind:      "log.new_template",
			Modality:  model.ModalityLog,
			Source:    "prod/" + service,
			Timestamp: at,
			Score:     0.7,
			Summary:   "new log template after learning period: " + id,
		}},
	}
}

func startFilterFixture(t *testing.T, events []model.Event) (*httptest.Server, *http.Client) {
	t.Helper()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}

	grpcServer := grpc.NewServer()
	tezcatlv1.RegisterAdminServiceServer(grpcServer, &fakeAdmin{events: events})
	go grpcServer.Serve(listener)
	t.Cleanup(grpcServer.Stop)

	cfg := itzconfig.Default()
	cfg.Tezcatl.Target = "tcp://" + listener.Addr().String()
	cfg.Auth.Password.Password = "sesame"
	// No caching, so a test can change what the server answers between
	// two requests.
	cfg.Incidents.CacheTTL = 0

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

	browser := &http.Client{Jar: jar}

	res, err := browser.PostForm(web.URL+"/login", url.Values{"password": {"sesame"}})
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()

	return web, browser
}

// TestIncidentListDefaultsToTodaysCriticals is the dashboard's opening
// question: what needs an answer now.
func TestIncidentListDefaultsToTodaysCriticals(t *testing.T) {
	now := time.Now()

	web, browser := startFilterFixture(t, []model.Event{
		anomaly("recent-critical", "checkout", model.SeverityCritical, now.Add(-2*time.Hour)),
		anomaly("recent-warning", "blog", model.SeverityWarning, now.Add(-3*time.Hour)),
		anomaly("old-critical", "ssh", model.SeverityCritical, now.Add(-72*time.Hour)),
	})

	_, body := get(t, browser, web.URL+"/")

	if !strings.Contains(body, "recent-critical") {
		t.Error("the default view must show today's criticals")
	}

	if strings.Contains(body, "recent-warning") {
		t.Error("the default view must not show warnings")
	}

	if strings.Contains(body, "old-critical") {
		t.Error("the default view must not reach past 24 hours")
	}

	if !strings.Contains(body, "dernières 24 h") || !strings.Contains(body, "critiques seulement") {
		t.Errorf("the subtitle must say what is being filtered: %s", body)
	}
}

func TestIncidentListFilters(t *testing.T) {
	now := time.Now()

	web, browser := startFilterFixture(t, []model.Event{
		anomaly("recent-critical", "checkout", model.SeverityCritical, now.Add(-2*time.Hour)),
		anomaly("recent-warning", "blog", model.SeverityWarning, now.Add(-3*time.Hour)),
		anomaly("old-critical", "ssh", model.SeverityCritical, now.Add(-72*time.Hour)),
	})

	for name, test := range map[string]struct {
		query   string
		present []string
		absent  []string
	}{
		"warnings and above": {
			query:   "?severity-set=warning",
			present: []string{"recent-critical", "recent-warning"},
			absent:  []string{"old-critical"},
		},
		"the whole window": {
			query:   "?range-set=all",
			present: []string{"recent-critical", "old-critical"},
			absent:  []string{"recent-warning"},
		},
		"one hour": {
			query:  "?range-set=1h",
			absent: []string{"recent-critical", "recent-warning", "old-critical"},
		},
		"everything": {
			query:   "?range-set=all&severity-set=all",
			present: []string{"recent-critical", "recent-warning", "old-critical"},
		},
	} {
		_, body := get(t, browser, web.URL+"/"+test.query)

		for _, expected := range test.present {
			if !strings.Contains(body, expected) {
				t.Errorf("%s: %q should be listed", name, expected)
			}
		}

		for _, unexpected := range test.absent {
			if strings.Contains(body, unexpected) {
				t.Errorf("%s: %q should not be listed", name, unexpected)
			}
		}
	}
}

// TestIncidentListGapRegroups checks the knob does what it says: two
// bursts forty minutes apart are one story or two, depending on how
// long a silence ends an incident.
func TestIncidentListGapRegroups(t *testing.T) {
	now := time.Now()

	web, browser := startFilterFixture(t, []model.Event{
		anomaly("first-burst", "checkout", model.SeverityCritical, now.Add(-2*time.Hour)),
		anomaly("second-burst", "checkout", model.SeverityCritical, now.Add(-80*time.Minute)),
	})

	_, tight := get(t, browser, web.URL+"/?gap-set=30m")
	if count := strings.Count(tight, "class=\"card\""); count != 2 {
		t.Errorf("a 30m gap must split the two bursts, got %d incident(s)", count)
	}

	_, loose := get(t, browser, web.URL+"/?gap-set=2h")
	if count := strings.Count(loose, "class=\"card\""); count != 1 {
		t.Errorf("a 2h gap must merge the two bursts, got %d incident(s)", count)
	}

	// The filter travels with the pagination link, or the second page
	// would silently answer a different question.
	if !strings.Contains(tight, "gap=30m") {
		if strings.Contains(tight, "Charger les incidents") {
			t.Error("the load-more link must carry the filter")
		}
	}
}

// TestIncidentFilterSurvivesTheDetailLink guards the link out of a
// narrowed list: the incident's identity depends on the gap, so the
// detail page must be read with the same one.
func TestIncidentFilterSurvivesTheDetailLink(t *testing.T) {
	now := time.Now()

	web, browser := startFilterFixture(t, []model.Event{
		anomaly("first-burst", "checkout", model.SeverityCritical, now.Add(-2*time.Hour)),
		anomaly("second-burst", "checkout", model.SeverityCritical, now.Add(-80*time.Minute)),
	})

	// With a loose gap the two bursts are one incident, named after
	// the first.
	status, body := get(t, browser, web.URL+"/incidents/first-burst?gap=2h")
	if status != http.StatusOK {
		t.Fatalf("detail with a loose gap = %d", status)
	}

	if !strings.Contains(body, "second-burst") {
		t.Error("the merged incident must carry both bursts")
	}
}
