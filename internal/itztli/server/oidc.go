package server

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	itzconfig "github.com/bornholm/tezcatl/internal/itztli/config"
	gooidc "github.com/coreos/go-oidc/v3/oidc"
	"github.com/pkg/errors"
	"golang.org/x/oauth2"
)

// oidcAuth drives the authorization-code flow against the configured
// issuer. Discovery is lazy and retried: itztli must be able to start
// while the identity provider is down.
type oidcAuth struct {
	config  itzconfig.OIDC
	baseURL string

	mutex    sync.Mutex
	provider *gooidc.Provider
	oauth    oauth2.Config
	verifier *gooidc.IDTokenVerifier
}

func newOIDCAuth(cfg itzconfig.OIDC, baseURL string) *oidcAuth {
	return &oidcAuth{
		config:  cfg,
		baseURL: strings.TrimSuffix(baseURL, "/"),
	}
}

// buttonLabel names the identity provider on the login button.
func (a *oidcAuth) buttonLabel() string {
	if a.config.ButtonLabel != "" {
		return a.config.ButtonLabel
	}

	if u, err := url.Parse(a.config.Issuer); err == nil && u.Host != "" {
		return "Se connecter via " + u.Host
	}

	return "Se connecter (OIDC)"
}

func (a *oidcAuth) ensure(ctx context.Context) error {
	a.mutex.Lock()
	defer a.mutex.Unlock()

	if a.provider != nil {
		return nil
	}

	provider, err := gooidc.NewProvider(ctx, a.config.Issuer)
	if err != nil {
		return errors.Wrapf(err, "could not discover the OIDC issuer %q", a.config.Issuer)
	}

	scopes := []string{gooidc.ScopeOpenID}
	for _, scope := range a.config.Scopes {
		if scope != gooidc.ScopeOpenID {
			scopes = append(scopes, scope)
		}
	}

	a.provider = provider
	a.oauth = oauth2.Config{
		ClientID:     a.config.ClientID,
		ClientSecret: a.config.ClientSecret,
		Endpoint:     provider.Endpoint(),
		RedirectURL:  a.baseURL + "/oidc/callback",
		Scopes:       scopes,
	}
	a.verifier = provider.Verifier(&gooidc.Config{ClientID: a.config.ClientID})

	return nil
}

const (
	stateCookie = "itztli_oidc_state"
	nonceCookie = "itztli_oidc_nonce"
)

// start sends the browser to the identity provider.
func (a *oidcAuth) start(w http.ResponseWriter, r *http.Request, secure bool) error {
	if err := a.ensure(r.Context()); err != nil {
		return errors.WithStack(err)
	}

	state, nonce := randomToken(), randomToken()

	for name, value := range map[string]string{stateCookie: state, nonceCookie: nonce} {
		http.SetCookie(w, &http.Cookie{
			Name:     name,
			Value:    value,
			Path:     "/",
			MaxAge:   int((10 * time.Minute).Seconds()),
			HttpOnly: true,
			Secure:   secure,
			SameSite: http.SameSiteLaxMode,
		})
	}

	http.Redirect(w, r, a.oauth.AuthCodeURL(state, gooidc.Nonce(nonce)), http.StatusFound)

	return nil
}

// callback validates the round trip and reports whether the user is
// authenticated.
func (a *oidcAuth) callback(w http.ResponseWriter, r *http.Request) error {
	if err := a.ensure(r.Context()); err != nil {
		return errors.WithStack(err)
	}

	state, err := r.Cookie(stateCookie)
	if err != nil || state.Value == "" || r.URL.Query().Get("state") != state.Value {
		return errors.New("state mismatch (expired or forged login round trip)")
	}

	nonce, err := r.Cookie(nonceCookie)
	if err != nil || nonce.Value == "" {
		return errors.New("missing nonce cookie (expired login round trip)")
	}

	for _, name := range []string{stateCookie, nonceCookie} {
		http.SetCookie(w, &http.Cookie{Name: name, Value: "", Path: "/", MaxAge: -1})
	}

	if errParam := r.URL.Query().Get("error"); errParam != "" {
		return errors.Errorf("the identity provider refused: %s", errParam)
	}

	token, err := a.oauth.Exchange(r.Context(), r.URL.Query().Get("code"))
	if err != nil {
		return errors.Wrap(err, "could not exchange the authorization code")
	}

	rawIDToken, ok := token.Extra("id_token").(string)
	if !ok {
		return errors.New("the token response carries no id_token")
	}

	idToken, err := a.verifier.Verify(r.Context(), rawIDToken)
	if err != nil {
		return errors.Wrap(err, "could not verify the id_token")
	}

	if idToken.Nonce != nonce.Value {
		return errors.New("nonce mismatch")
	}

	return nil
}
