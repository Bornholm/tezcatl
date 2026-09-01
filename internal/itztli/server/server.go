// Package server is the HTTP face of itztli: routes, sessions and
// authentication in front of the tezcatl AdminService.
package server

import (
	"context"
	"crypto/subtle"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/a-h/templ"
	"github.com/bornholm/tezcatl/internal/core/incident"
	itzclient "github.com/bornholm/tezcatl/internal/itztli/client"
	itzconfig "github.com/bornholm/tezcatl/internal/itztli/config"
	"github.com/bornholm/tezcatl/internal/itztli/explain"
	"github.com/bornholm/tezcatl/internal/itztli/web"
	"golang.org/x/crypto/bcrypt"
)

const (
	sessionCookie = "itztli_session"
	// templatePageSize bounds the templates list; a busy server learns
	// hundreds of them.
	templatePageSize = 50
	// explainTimeout bounds one LLM round trip.
	explainTimeout = 2 * time.Minute
	// explainRetention is how long a finished explanation stays
	// available, so a reload or a second reader finds the answer
	// instead of paying for it again.
	explainRetention = 30 * time.Minute
)

type Server struct {
	config    *itzconfig.Config
	client    *itzclient.Client
	explainer *explain.Explainer
	sessions  *sessionStore
	explains  *explainJobs
	oidc      *oidcAuth
	version   string
	logger    *slog.Logger
}

func New(cfg *itzconfig.Config, client *itzclient.Client, explainer *explain.Explainer, version string, logger *slog.Logger) *Server {
	server := &Server{
		config:    cfg,
		client:    client,
		explainer: explainer,
		sessions:  newSessionStore(cfg.Auth.SessionTTL.AsDuration()),
		explains:  newExplainJobs(explainRetention),
		version:   version,
		logger:    logger,
	}

	if cfg.Auth.Mode == "oidc" {
		server.oidc = newOIDCAuth(cfg.Auth.OIDC, cfg.Server.BaseURL)
	}

	return server
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	mux.Handle("GET /static/", web.StaticHandler())
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintln(w, "ok")
	})

	mux.HandleFunc("GET /login", s.handleLoginPage)
	mux.HandleFunc("POST /login", s.handleLoginSubmit)
	mux.HandleFunc("POST /logout", s.handleLogout)
	mux.HandleFunc("GET /oidc/start", s.handleOIDCStart)
	mux.HandleFunc("GET /oidc/callback", s.handleOIDCCallback)

	mux.HandleFunc("GET /{$}", s.authed(s.handleIncidents))
	mux.HandleFunc("GET /incidents/rows", s.authed(s.handleIncidentRows))
	mux.HandleFunc("GET /incidents/{id}", s.authed(s.handleIncidentDetail))
	mux.HandleFunc("POST /incidents/{id}/explain", s.authed(s.handleExplain))
	mux.HandleFunc("GET /incidents/{id}/explain/status", s.authed(s.handleExplainStatus))
	mux.HandleFunc("GET /incidents/{id}/explain/reset", s.authed(s.handleExplainReset))
	mux.HandleFunc("GET /templates", s.authed(s.handleTemplates))
	mux.HandleFunc("GET /templates/rows", s.authed(s.handleTemplateRows))
	mux.HandleFunc("POST /templates/mark", s.authed(s.handleTemplateMark))
	mux.HandleFunc("GET /metrics", s.authed(s.handleMetrics))
	mux.HandleFunc("POST /metrics/mark", s.authed(s.handleMetricMark))

	return s.withHeaders(mux)
}

// withHeaders sets the security headers on every response. The CSP
// allows exactly what the pages use: local assets, inline style
// attributes, and the Google Fonts pair.
func (s *Server) withHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		header := w.Header()
		header.Set("Content-Security-Policy",
			"default-src 'self'; style-src 'self' 'unsafe-inline' https://fonts.googleapis.com; "+
				"font-src https://fonts.gstatic.com; script-src 'self'; img-src 'self' data:; "+
				"connect-src 'self'; frame-ancestors 'none'")
		header.Set("X-Content-Type-Options", "nosniff")
		header.Set("Referrer-Policy", "same-origin")

		next.ServeHTTP(w, r)
	})
}

// --- Authentication ------------------------------------------------------

func (s *Server) authed(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		cookie, err := r.Cookie(sessionCookie)
		if err != nil || !s.sessions.valid(cookie.Value) {
			// htmx swaps a redirect's body into the page fragment; the
			// HX-Redirect header makes it navigate instead.
			if r.Header.Get("HX-Request") == "true" {
				w.Header().Set("HX-Redirect", "/login")
				w.WriteHeader(http.StatusUnauthorized)

				return
			}

			http.Redirect(w, r, "/login", http.StatusFound)

			return
		}

		next(w, r)
	}
}

// secureCookies reports whether session cookies must be Secure: yes as
// soon as the public URL is https, whatever the local hop looks like.
func (s *Server) secureCookies() bool {
	return strings.HasPrefix(s.config.Server.BaseURL, "https://")
}

func (s *Server) openSession(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    s.sessions.create(),
		Path:     "/",
		MaxAge:   int(s.config.Auth.SessionTTL.AsDuration().Seconds()),
		HttpOnly: true,
		Secure:   s.secureCookies(),
		SameSite: http.SameSiteLaxMode,
	})
}

func (s *Server) loginView(errorMessage string) web.Login {
	login := web.Login{Mode: s.config.Auth.Mode, Error: errorMessage}
	if s.oidc != nil {
		login.ButtonLabel = s.oidc.buttonLabel()
	}

	return login
}

func (s *Server) handleLoginPage(w http.ResponseWriter, r *http.Request) {
	s.render(w, r, web.LoginPage(s.loginView("")))
}

func (s *Server) handleLoginSubmit(w http.ResponseWriter, r *http.Request) {
	if s.config.Auth.Mode != "password" {
		http.Redirect(w, r, "/login", http.StatusFound)

		return
	}

	if !s.checkPassword(r.FormValue("password")) {
		s.logger.Warn("failed password login", "remote", r.RemoteAddr)
		w.WriteHeader(http.StatusUnauthorized)
		s.render(w, r, web.LoginPage(s.loginView("Mot de passe incorrect.")))

		return
	}

	s.openSession(w)
	http.Redirect(w, r, "/", http.StatusFound)
}

func (s *Server) checkPassword(password string) bool {
	if hash := s.config.Auth.Password.PasswordHash; hash != "" {
		return bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) == nil
	}

	expected := s.config.Auth.Password.Password

	return expected != "" && subtle.ConstantTimeCompare([]byte(expected), []byte(password)) == 1
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if cookie, err := r.Cookie(sessionCookie); err == nil {
		s.sessions.revoke(cookie.Value)
	}

	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: "", Path: "/", MaxAge: -1})
	http.Redirect(w, r, "/login", http.StatusFound)
}

func (s *Server) handleOIDCStart(w http.ResponseWriter, r *http.Request) {
	if s.oidc == nil {
		http.Redirect(w, r, "/login", http.StatusFound)

		return
	}

	if err := s.oidc.start(w, r, s.secureCookies()); err != nil {
		s.logger.Error("oidc start failed", "error", err)
		s.render(w, r, web.LoginPage(s.loginView("Le fournisseur d'identité est injoignable.")))
	}
}

func (s *Server) handleOIDCCallback(w http.ResponseWriter, r *http.Request) {
	if s.oidc == nil {
		http.Redirect(w, r, "/login", http.StatusFound)

		return
	}

	if err := s.oidc.callback(w, r); err != nil {
		s.logger.Warn("oidc callback rejected", "error", err)
		w.WriteHeader(http.StatusUnauthorized)
		s.render(w, r, web.LoginPage(s.loginView("La connexion a été refusée par le fournisseur d'identité.")))

		return
	}

	s.openSession(w)
	http.Redirect(w, r, "/", http.StatusFound)
}

// --- Incidents -----------------------------------------------------------

func (s *Server) nav(active string) web.Nav {
	return web.Nav{
		Version: s.version,
		Target:  s.config.Tezcatl.Target,
		Window:  s.config.Incidents.Window.AsDuration(),
		Active:  active,
	}
}

func (s *Server) handleIncidents(w http.ResponseWriter, r *http.Request) {
	snapshot, err := s.client.Incidents(r.Context())
	if err != nil {
		s.renderUpstreamError(w, r, "incidents", err)

		return
	}

	nav := s.nav("incidents")

	if len(snapshot.Incidents) == 0 {
		empty := s.emptyState(r.Context(), snapshot)
		context := nav.WindowContext() + " · aucun incident"
		s.render(w, r, web.IncidentsPage(nav, context, nil, 0, false, &empty))

		return
	}

	cards, nextOffset, more := s.incidentCards(snapshot, 0)
	context := fmt.Sprintf("%s · %s · %s chargés",
		nav.WindowContext(),
		web.Plural(len(snapshot.Incidents), "incident"),
		web.Plural(snapshot.TotalEvents, "événement"))

	s.render(w, r, web.IncidentsPage(nav, context, cards, nextOffset, more, nil))
}

func (s *Server) handleIncidentRows(w http.ResponseWriter, r *http.Request) {
	snapshot, err := s.client.Incidents(r.Context())
	if err != nil {
		s.renderUpstreamError(w, r, "incidents", err)

		return
	}

	offset := parseOffset(r.URL.Query().Get("offset"))
	cards, nextOffset, more := s.incidentCards(snapshot, offset)

	s.render(w, r, web.IncidentBatch(cards, nextOffset, more))
}

func (s *Server) incidentCards(snapshot *itzclient.Snapshot, offset int) ([]web.IncidentCard, int, bool) {
	pageSize := s.config.Incidents.PageSize
	now := time.Now()

	if offset > len(snapshot.Incidents) {
		offset = len(snapshot.Incidents)
	}

	end := offset + pageSize
	if end > len(snapshot.Incidents) {
		end = len(snapshot.Incidents)
	}

	cards := make([]web.IncidentCard, 0, end-offset)
	for _, entry := range snapshot.Incidents[offset:end] {
		cards = append(cards, web.NewIncidentCard(entry, now))
	}

	return cards, end, end < len(snapshot.Incidents)
}

// emptyState gathers the honest numbers behind "no incident". Best
// effort: a failure to count templates must not break the page.
func (s *Server) emptyState(ctx context.Context, snapshot *itzclient.Snapshot) web.EmptyState {
	empty := web.EmptyState{TotalEvents: snapshot.TotalEvents}

	if templates, err := s.client.Templates(ctx); err == nil {
		empty.Templates = len(templates)
	}

	if metrics, err := s.client.Metrics(ctx); err == nil {
		for _, metric := range metrics {
			if metric.Warmup {
				empty.WarmupMetrics++
			}
		}
	}

	return empty
}

func (s *Server) handleIncidentDetail(w http.ResponseWriter, r *http.Request) {
	entry, found, err := s.client.Incident(r.Context(), r.PathValue("id"))
	if err != nil {
		s.renderUpstreamError(w, r, "incidents", err)

		return
	}

	if !found {
		// The snapshot moved on and the incident left the window.
		http.Redirect(w, r, "/", http.StatusFound)

		return
	}

	s.render(w, r, web.DetailPage(s.nav("incidents"), web.NewIncidentDetail(entry), s.explainView(r.PathValue("id"))))
}

// explainView reads the current state of an incident's explanation.
func (s *Server) explainView(incidentID string) web.ExplainView {
	view := web.ExplainView{IncidentID: incidentID, State: web.ExplainIdle}

	if s.explainer == nil {
		return view
	}

	view.Model = s.explainer.Model()

	job, exists := s.explains.get(incidentID)
	switch {
	case !exists:
	case !job.Done:
		view.State = web.ExplainPendingState
	default:
		view.State = web.ExplainDone
		view.Text = job.Text
		view.Error = job.Err
	}

	return view
}

// handleExplain starts a generation and answers immediately with the
// pending panel. The model's own pace is nobody's HTTP timeout: the
// work runs past this request and the browser polls for it.
func (s *Server) handleExplain(w http.ResponseWriter, r *http.Request) {
	if s.explainer == nil {
		http.NotFound(w, r)

		return
	}

	incidentID := r.PathValue("id")

	if s.explains.start(incidentID) {
		entry, found, err := s.client.Incident(r.Context(), incidentID)
		switch {
		case err != nil:
			s.explains.finish(incidentID, "", err.Error())
		case !found:
			s.explains.finish(incidentID, "", "l'incident n'est plus dans la fenêtre du serveur")
		default:
			go s.runExplain(incidentID, entry)
		}
	}

	view := s.explainView(incidentID)

	s.render(w, r, web.ExplainZone(view), web.ExplainButtonSlot(view, true))
}

// runExplain generates outside any request: its context is the
// server's, not the browser's, so a reader who closes the tab does not
// cancel a call already paid for.
func (s *Server) runExplain(incidentID string, entry incident.Incident) {
	ctx, cancel := context.WithTimeout(context.Background(), explainTimeout)
	defer cancel()

	text, err := s.explainer.Explain(ctx, entry)
	if err != nil {
		s.logger.Error("explain failed", "incident", incidentID, "error", err)
		s.explains.finish(incidentID, "", err.Error())

		return
	}

	s.explains.finish(incidentID, text, "")
}

// handleExplainStatus answers the pending panel's poll.
func (s *Server) handleExplainStatus(w http.ResponseWriter, r *http.Request) {
	s.render(w, r, web.ExplainZone(s.explainView(r.PathValue("id"))))
}

func (s *Server) handleExplainReset(w http.ResponseWriter, r *http.Request) {
	incidentID := r.PathValue("id")

	s.explains.forget(incidentID)

	view := s.explainView(incidentID)

	s.render(w, r, web.ExplainZone(view), web.ExplainButtonSlot(view, true))
}

// --- Templates -----------------------------------------------------------

func (s *Server) handleTemplates(w http.ResponseWriter, r *http.Request) {
	filter := templateFilter(r)

	templates, err := s.client.Templates(r.Context())
	if err != nil {
		s.renderUpstreamError(w, r, "templates", err)

		return
	}

	rows, nextOffset, more := templateRows(templates, filter, parseOffset(r.URL.Query().Get("offset")))
	nav := s.nav("templates")
	context := fmt.Sprintf("%s appris · les surlignages sont des masques, pas des valeurs",
		web.Plural(len(templates), "template"))

	s.render(w, r, web.TemplatesPage(nav, context, filter, rows, nextOffset, more))
}

func (s *Server) handleTemplateRows(w http.ResponseWriter, r *http.Request) {
	filter := templateFilter(r)

	templates, err := s.client.Templates(r.Context())
	if err != nil {
		s.renderUpstreamError(w, r, "templates", err)

		return
	}

	rows, nextOffset, more := templateRows(templates, filter, parseOffset(r.URL.Query().Get("offset")))

	// A chip click changes the filter: move the active state and the
	// hidden field along with the rows, out of band.
	if r.URL.Query().Has("marking-set") {
		s.render(w, r, web.TemplateBatch(filter, rows, nextOffset, more),
			web.TemplateChips(filter.Marking, true),
			web.TemplateMarkingField(filter.Marking))

		return
	}

	s.render(w, r, web.TemplateBatch(filter, rows, nextOffset, more))
}

func templateFilter(r *http.Request) web.TemplateFilter {
	query := r.URL.Query()

	marking := query.Get("marking")
	if set := query.Get("marking-set"); set != "" {
		marking = set
	}
	if marking == "" {
		marking = "tous"
	}

	return web.TemplateFilter{
		Query:   strings.TrimSpace(query.Get("q")),
		Marking: marking,
	}
}

func templateRows(templates []itzclient.Template, filter web.TemplateFilter, offset int) ([]web.TemplateRow, int, bool) {
	needle := strings.ToLower(filter.Query)

	kept := make([]itzclient.Template, 0, len(templates))
	for _, template := range templates {
		if needle != "" &&
			!strings.Contains(strings.ToLower(template.Template), needle) &&
			!strings.Contains(strings.ToLower(template.Partition), needle) {
			continue
		}

		switch filter.Marking {
		case "tous":
		case "aucun":
			if template.Marking != "" {
				continue
			}
		default:
			if template.Marking != filter.Marking {
				continue
			}
		}

		kept = append(kept, template)
	}

	if offset > len(kept) {
		offset = len(kept)
	}

	end := offset + templatePageSize
	if end > len(kept) {
		end = len(kept)
	}

	rows := make([]web.TemplateRow, 0, end-offset)
	for _, template := range kept[offset:end] {
		rows = append(rows, web.NewTemplateRow(template))
	}

	return rows, end, end < len(kept)
}

func (s *Server) handleTemplateMark(w http.ResponseWriter, r *http.Request) {
	template := r.FormValue("template")
	marking := r.FormValue("marking")

	if template == "" || !validMarking(marking) {
		http.Error(w, "malformed marking request", http.StatusBadRequest)

		return
	}

	if err := s.client.MarkTemplate(r.Context(), template, marking); err != nil {
		s.renderUpstreamError(w, r, "templates", err)

		return
	}

	// The marking changes what the server reports: the next incident
	// snapshot must see it.
	s.client.Invalidate()

	s.render(w, r, web.TemplateRowView(web.NewTemplateRow(itzclient.Template{
		Partition: r.FormValue("partition"),
		Template:  template,
		Size:      parseSize(r.FormValue("size")),
		Marking:   marking,
	})))
}

func validMarking(marking string) bool {
	switch marking {
	case "", "ignore", "normal", "symptomatic":
		return true
	default:
		return false
	}
}

// --- Metrics -------------------------------------------------------------

func (s *Server) handleMetrics(w http.ResponseWriter, r *http.Request) {
	metrics, err := s.client.Metrics(r.Context())
	if err != nil {
		s.renderUpstreamError(w, r, "metrics", err)

		return
	}

	rows := make([]web.MetricRow, 0, len(metrics))
	for _, metric := range metrics {
		rows = append(rows, web.NewMetricRow(metric))
	}

	nav := s.nav("metrics")
	context := fmt.Sprintf("%s apprises · pas d'historique de valeurs : moyenne, écart type et niveau récent seulement",
		web.Plural(len(metrics), "série"))

	s.render(w, r, web.MetricsPage(nav, context, rows))
}

func (s *Server) handleMetricMark(w http.ResponseWriter, r *http.Request) {
	pattern := r.FormValue("pattern")
	ignore := r.FormValue("ignore") == "true"

	if pattern == "" {
		http.Error(w, "malformed marking request", http.StatusBadRequest)

		return
	}

	if err := s.client.MarkMetric(r.Context(), pattern, ignore); err != nil {
		s.renderUpstreamError(w, r, "metrics", err)

		return
	}

	s.client.Invalidate()

	// Re-read the series so the row shows the server's own view of the
	// marking, not an optimistic echo.
	metrics, err := s.client.Metrics(r.Context())
	if err != nil {
		s.renderUpstreamError(w, r, "metrics", err)

		return
	}

	for _, metric := range metrics {
		if metric.Key == pattern {
			s.render(w, r, web.MetricRowView(web.NewMetricRow(metric)))

			return
		}
	}

	// The series vanished between the mark and the re-read; drop the
	// row rather than invent one.
	w.WriteHeader(http.StatusOK)
}

// --- Rendering -----------------------------------------------------------

// render writes the components in order; secondary ones are htmx
// out-of-band fragments.
func (s *Server) render(w http.ResponseWriter, r *http.Request, components ...templ.Component) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")

	for _, component := range components {
		if err := component.Render(r.Context(), w); err != nil {
			s.logger.Error("render failed", "path", r.URL.Path, "error", err)

			return
		}
	}
}

// renderUpstreamError says plainly that the tezcatl server did not
// answer. Full pages get the framed error; htmx fragments get a bare
// message where they would have swapped.
func (s *Server) renderUpstreamError(w http.ResponseWriter, r *http.Request, active string, err error) {
	s.logger.Error("tezcatl server unreachable", "target", s.config.Tezcatl.Target, "error", err)

	w.WriteHeader(http.StatusBadGateway)

	if r.Header.Get("HX-Request") == "true" {
		fmt.Fprintln(w, "Le serveur tezcatl est injoignable.")

		return
	}

	s.render(w, r, web.ErrorPage(s.nav(active), err.Error()))
}

func parseOffset(raw string) int {
	offset := 0
	fmt.Sscanf(raw, "%d", &offset)
	if offset < 0 {
		offset = 0
	}

	return offset
}

func parseSize(raw string) int64 {
	var size int64
	fmt.Sscanf(raw, "%d", &size)

	return size
}

// ListenAndServe runs until the context ends.
func (s *Server) ListenAndServe(ctx context.Context) error {
	httpServer := &http.Server{
		Addr:              s.config.Server.Listen,
		Handler:           s.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	go func() {
		<-ctx.Done()

		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		httpServer.Shutdown(shutdownCtx)
	}()

	s.logger.Info("itztli listening", "addr", s.config.Server.Listen, "target", s.config.Tezcatl.Target)

	if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		return err
	}

	return nil
}
