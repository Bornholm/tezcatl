package server

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
)

// TestIncidentMarkShortcut covers the feedback loop where the
// judgement is actually made: the reader has the incident in front of
// them and says whether the pattern is noise or a symptom.
func TestIncidentMarkShortcut(t *testing.T) {
	fake, web, browser := startFixture(t)
	login(t, web, browser)

	status, body := get(t, browser, web.URL+"/incidents/ev-trigger")
	if status != http.StatusOK {
		t.Fatalf("GET detail = %d", status)
	}

	if !strings.Contains(body, `hx-post="/incidents/mark"`) ||
		!strings.Contains(body, "symptomatique") {
		t.Fatalf("the detail page must offer the marking shortcut: %s", body)
	}

	// The server already holds this template as "normal": the shortcut
	// must show the marking in force rather than a blank slate.
	if !strings.Contains(body, `value="HTTP server listening on &lt;IP&gt;:&lt;NUM&gt;"`) {
		t.Error("the shortcut must target the template behind the trigger")
	}

	res, err := browser.PostForm(web.URL+"/incidents/mark", url.Values{
		"kind":    {"template"},
		"target":  {"HTTP server listening on <IP>:<NUM>"},
		"marking": {"symptomatic"},
	})
	if err != nil {
		t.Fatal(err)
	}
	fragment := readBody(t, res)

	if res.StatusCode != http.StatusOK {
		t.Fatalf("mark = %d", res.StatusCode)
	}

	if got := fake.markedTemplates["HTTP server listening on <IP>:<NUM>"]; got != "symptomatic" {
		t.Fatalf("the marking did not reach the server, got %q", got)
	}

	if !strings.Contains(fragment, `class="chip-btn active"`) {
		t.Errorf("the returned shortcut must show the new marking: %s", fragment)
	}

	// Back to the default, the same way.
	res, err = browser.PostForm(web.URL+"/incidents/mark", url.Values{
		"kind":    {"template"},
		"target":  {"HTTP server listening on <IP>:<NUM>"},
		"marking": {""},
	})
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()

	if got := fake.markedTemplates["HTTP server listening on <IP>:<NUM>"]; got != "" {
		t.Fatalf("clearing the marking failed, got %q", got)
	}
}

// TestIncidentMarkRefusesNonsense keeps the shortcut honest: a series
// can only be ignored, and an unknown marking is a bad request rather
// than something forwarded to the server.
func TestIncidentMarkRefusesNonsense(t *testing.T) {
	fake, web, browser := startFixture(t)
	login(t, web, browser)

	for name, form := range map[string]url.Values{
		"a symptomatic series": {
			"kind": {"metric"}, "target": {"prod/api/queue_depth"}, "marking": {"symptomatic"},
		},
		"an unknown marking": {
			"kind": {"template"}, "target": {"whatever"}, "marking": {"louder"},
		},
		"no target": {
			"kind": {"template"}, "target": {""}, "marking": {"ignore"},
		},
		"an unknown kind": {
			"kind": {"trace"}, "target": {"whatever"}, "marking": {"ignore"},
		},
	} {
		res, err := browser.PostForm(web.URL+"/incidents/mark", form)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()

		if res.StatusCode != http.StatusBadRequest {
			t.Errorf("%s: got %d, want 400", name, res.StatusCode)
		}
	}

	if len(fake.markedTemplates) != 0 || len(fake.markedMetrics) != 0 {
		t.Error("a rejected request must not reach the server")
	}
}

// TestIncidentMarkSeries checks the metric side, which the shortcut
// reaches by the series key carried on the signal.
func TestIncidentMarkSeries(t *testing.T) {
	fake, web, browser := startFixture(t)
	login(t, web, browser)

	res, err := browser.PostForm(web.URL+"/incidents/mark", url.Values{
		"kind":    {"metric"},
		"target":  {"production/host/system.cpu.percent"},
		"marking": {"ignore"},
	})
	if err != nil {
		t.Fatal(err)
	}
	fragment := readBody(t, res)

	if res.StatusCode != http.StatusOK {
		t.Fatalf("mark series = %d", res.StatusCode)
	}

	if !fake.markedMetrics["production/host/system.cpu.percent"] {
		t.Fatal("the ignore marking did not reach the server")
	}

	// A series is either ignored or not: the shortcut must not offer a
	// marking the API has no meaning for.
	if strings.Contains(fragment, "symptomatique") {
		t.Errorf("a series must not be offered as symptomatic: %s", fragment)
	}
}

func readBody(t *testing.T, res *http.Response) string {
	t.Helper()

	defer res.Body.Close()

	body := make([]byte, 0, 4096)
	buffer := make([]byte, 1024)

	for {
		n, err := res.Body.Read(buffer)
		body = append(body, buffer[:n]...)

		if err != nil {
			break
		}
	}

	return string(body)
}
