package server

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	itzconfig "github.com/bornholm/tezcatl/internal/itztli/config"
)

// stubIssuer serves just enough OIDC discovery for the start of the
// authorization-code flow.
func stubIssuer(t *testing.T) *httptest.Server {
	t.Helper()

	mux := http.NewServeMux()
	issuer := httptest.NewServer(mux)
	t.Cleanup(issuer.Close)

	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"issuer":                 issuer.URL,
			"authorization_endpoint": issuer.URL + "/auth",
			"token_endpoint":         issuer.URL + "/token",
			"jwks_uri":               issuer.URL + "/jwks",
		})
	})

	return issuer
}

func TestOIDCStartRedirectsToTheProvider(t *testing.T) {
	issuer := stubIssuer(t)

	cfg := itzconfig.Default()
	cfg.Auth.Mode = "oidc"
	cfg.Auth.OIDC = itzconfig.OIDC{
		Issuer:   issuer.URL,
		ClientID: "itztli",
	}
	cfg.Server.BaseURL = "http://itztli.local"

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	web := httptest.NewServer(New(cfg, nil, nil, "test", logger).Handler())
	t.Cleanup(web.Close)

	browser := &http.Client{
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}

	// The login page offers exactly one way in.
	res, err := browser.Get(web.URL + "/login")
	if err != nil {
		t.Fatal(err)
	}
	page, _ := io.ReadAll(res.Body)
	res.Body.Close()

	if !strings.Contains(string(page), "/oidc/start") || strings.Contains(string(page), "Mot de passe") {
		t.Fatal("the OIDC login page must offer the OIDC button and no password form")
	}

	res, err = browser.Get(web.URL + "/oidc/start")
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()

	if res.StatusCode != http.StatusFound {
		t.Fatalf("start = %d, want a redirect", res.StatusCode)
	}

	location, err := url.Parse(res.Header.Get("Location"))
	if err != nil {
		t.Fatal(err)
	}

	if !strings.HasPrefix(location.String(), issuer.URL+"/auth") {
		t.Fatalf("redirects to %s, want the issuer's authorization endpoint", location)
	}

	query := location.Query()
	if query.Get("client_id") != "itztli" || query.Get("state") == "" || query.Get("nonce") == "" {
		t.Errorf("authorization URL misses client_id, state or nonce: %s", location)
	}

	if query.Get("redirect_uri") != "http://itztli.local/oidc/callback" {
		t.Errorf("redirect_uri = %q", query.Get("redirect_uri"))
	}

	var stateCookieSet bool
	for _, cookie := range res.Cookies() {
		if cookie.Name == stateCookie && cookie.Value != "" && cookie.HttpOnly {
			stateCookieSet = true
		}
	}

	if !stateCookieSet {
		t.Error("the state cookie must be set, HttpOnly")
	}

	// A forged callback without the round-trip cookies is rejected.
	res, err = browser.Get(web.URL + "/oidc/callback?state=forged&code=x")
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()

	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("forged callback = %d, want 401", res.StatusCode)
	}
}
