package tui

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/bornholm/tezcatl/internal/core/admin"
	"github.com/bornholm/tezcatl/internal/core/detect"
	"github.com/bornholm/tezcatl/internal/core/model"
	"github.com/gdamore/tcell/v2"
	"github.com/pkg/errors"
	"github.com/rivo/tview"
)

// Source provides the data shown and mutated by the top view.
type Source interface {
	Templates(ctx context.Context) ([]admin.TemplateInfo, error)
	Metrics(ctx context.Context) ([]detect.SeriesInfo, error)
	Mark(ctx context.Context, template string, marking detect.Marking) error
	// MarkMetric silences (or restores) the series matching pattern.
	MarkMetric(ctx context.Context, pattern string, ignore bool) error
	// Events follows the published events, starting with the recent
	// ones. It calls connected once the stream is established, and
	// returns when the context is cancelled or the server goes away.
	Events(ctx context.Context, history int, out chan<- model.Event, connected func()) error
	// ListEvents returns past events from the server's persistent log,
	// oldest first, so the view has history beyond the live ring.
	ListEvents(ctx context.Context, limit int) ([]model.Event, error)
}

type Options struct {
	Target  string
	Version string
	Refresh time.Duration
}

const (
	pageEvents    = "events"
	pageTemplates = "templates"
	pageMetrics   = "metrics"
	pageDetail    = "detail"

	requestTimeout = 10 * time.Second
	messageTTL     = 5 * time.Second
	// eventHistory is how many past events to ask the server for, and
	// how many the view keeps in memory.
	eventHistory = 500
)

// eventReconnectDelay spaces out reconnection attempts when the server
// restarts under us. It is a variable so tests can shorten it.
var eventReconnectDelay = 3 * time.Second

type top struct {
	app    *tview.Application
	pages  *tview.Pages
	status *tview.TextView
	filter *tview.InputField

	eventsTable    *tview.Table
	templatesTable *tview.Table
	metricsTable   *tview.Table

	source Source
	opts   Options

	mu        sync.Mutex
	templates []admin.TemplateInfo
	metrics   []detect.SeriesInfo
	events    []model.Event
	// seen dedups events between the persistent history and the live
	// stream, which overlap on purpose rather than risking a gap.
	seen map[string]bool
	// visible mirrors the rows currently shown by templatesTable, and
	// visibleEvents those of eventsTable, so key handlers can map a
	// selected row back to its object.
	visible       []admin.TemplateInfo
	visibleEvents  []model.Event
	visibleMetrics []metricRow
	query         string
	fetchedAt     time.Time
	streaming     bool
	message       string
	messageAt     time.Time
}

// Run displays the interactive top view until the user quits or the
// context is cancelled.
func Run(ctx context.Context, source Source, opts Options) error {
	if opts.Refresh <= 0 {
		opts.Refresh = 3 * time.Second
	}

	t := &top{source: source, opts: opts}
	t.build()

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	go t.refreshLoop(ctx)
	go t.streamLoop(ctx)
	go func() {
		<-ctx.Done()
		t.app.Stop()
	}()

	return errors.WithStack(t.app.Run())
}

func (t *top) build() {
	t.app = tview.NewApplication()

	header := tview.NewTextView().SetDynamicColors(true)
	fmt.Fprintf(header, " [::b]tezcatl top[::-] %s  [gray]target=%s refresh=%s[-]\n",
		t.opts.Version, tview.Escape(t.opts.Target), t.opts.Refresh)
	fmt.Fprintf(header, " [yellow]1[-] events  [yellow]2[-] templates  [yellow]3[-] metrics  [yellow]/[-] filter  [yellow]n/i/s/c[-] mark  [yellow]enter[-] detail  [yellow]r[-] refresh  [yellow]q[-] quit")

	t.eventsTable = newTable()
	t.eventsTable.SetSelectedFunc(func(row, col int) { t.showDetail() })
	t.templatesTable = newTable()
	t.templatesTable.SetSelectedFunc(func(row, col int) { t.showDetail() })
	t.metricsTable = newTable()

	t.pages = tview.NewPages().
		AddPage(pageEvents, t.eventsTable, true, true).
		AddPage(pageTemplates, t.templatesTable, true, false).
		AddPage(pageMetrics, t.metricsTable, true, false)

	t.filter = tview.NewInputField().SetLabel(" / ")
	t.filter.SetChangedFunc(func(text string) {
		t.mu.Lock()
		t.query = text
		t.mu.Unlock()

		t.render()
	})
	t.filter.SetDoneFunc(func(key tcell.Key) {
		if key == tcell.KeyEscape {
			t.filter.SetText("")
		}

		t.app.SetFocus(t.currentTable())
	})

	t.status = tview.NewTextView().SetDynamicColors(true)

	layout := tview.NewFlex().SetDirection(tview.FlexRow).
		AddItem(header, 2, 0, false).
		AddItem(t.pages, 0, 1, true).
		AddItem(t.filter, 1, 0, false).
		AddItem(t.status, 1, 0, false)

	t.app.SetInputCapture(t.onKey)
	t.app.SetRoot(layout, true)
}

func newTable() *tview.Table {
	return tview.NewTable().
		SetFixed(1, 0).
		SetSelectable(true, false)
}

func (t *top) onKey(event *tcell.EventKey) *tcell.EventKey {
	if t.app.GetFocus() == t.filter {
		return event
	}

	if name, _ := t.pages.GetFrontPage(); name == pageDetail {
		if event.Key() == tcell.KeyEscape || event.Rune() == 'q' {
			t.pages.RemovePage(pageDetail)
			t.app.SetFocus(t.currentTable())

			return nil
		}

		return event
	}

	switch event.Rune() {
	case 'q':
		t.app.Stop()
		return nil
	case '1':
		t.pages.SwitchToPage(pageEvents)
		t.app.SetFocus(t.eventsTable)
		return nil
	case '2':
		t.pages.SwitchToPage(pageTemplates)
		t.app.SetFocus(t.templatesTable)
		return nil
	case '3':
		t.pages.SwitchToPage(pageMetrics)
		t.app.SetFocus(t.metricsTable)
		return nil
	case '/':
		t.app.SetFocus(t.filter)
		return nil
	case 'r':
		go t.refresh()
		return nil
	case 'n':
		t.markSelected(detect.MarkingNormal)
		return nil
	case 'i':
		t.markSelected(detect.MarkingIgnore)
		return nil
	case 's':
		t.markSelected(detect.MarkingSymptomatic)
		return nil
	case 'c':
		t.markSelected("")
		return nil
	}

	return event
}

func (t *top) currentTable() tview.Primitive {
	switch name, _ := t.pages.GetFrontPage(); name {
	case pageMetrics:
		return t.metricsTable
	case pageTemplates:
		return t.templatesTable
	default:
		return t.eventsTable
	}
}

// selectedEvent returns the event behind the selected table row.
func (t *top) selectedEvent() (model.Event, bool) {
	if name, _ := t.pages.GetFrontPage(); name != pageEvents {
		return model.Event{}, false
	}

	row, _ := t.eventsTable.GetSelection()

	t.mu.Lock()
	defer t.mu.Unlock()

	index := row - 1
	if index < 0 || index >= len(t.visibleEvents) {
		return model.Event{}, false
	}

	return t.visibleEvents[index], true
}

// selectedTemplate returns the template behind the selected table row.
func (t *top) selectedTemplate() (admin.TemplateInfo, bool) {
	if name, _ := t.pages.GetFrontPage(); name != pageTemplates {
		return admin.TemplateInfo{}, false
	}

	row, _ := t.templatesTable.GetSelection()

	t.mu.Lock()
	defer t.mu.Unlock()

	index := row - 1
	if index < 0 || index >= len(t.visible) {
		return admin.TemplateInfo{}, false
	}

	return t.visible[index], true
}

// selectedMetric returns the series behind the selected table row.
func (t *top) selectedMetric() (metricRow, bool) {
	if name, _ := t.pages.GetFrontPage(); name != pageMetrics {
		return metricRow{}, false
	}

	row, _ := t.metricsTable.GetSelection()

	t.mu.Lock()
	defer t.mu.Unlock()

	index := row - 1
	if index < 0 || index >= len(t.visibleMetrics) {
		return metricRow{}, false
	}

	return t.visibleMetrics[index], true
}

func (t *top) markSelected(marking detect.Marking) {
	// On the metrics view, i silences the selected series and c clears
	// the marking; the other markings have no metric meaning.
	if metric, ok := t.selectedMetric(); ok {
		if marking != detect.MarkingIgnore && marking != "" {
			return
		}

		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
			defer cancel()

			if err := t.source.MarkMetric(ctx, metric.Key, marking == detect.MarkingIgnore); err != nil {
				t.setMessage(fmt.Sprintf("[red]mark failed: %v[-]", err))

				return
			}

			label := "ignored"
			if marking == "" {
				label = "cleared"
			}

			t.setMessage(fmt.Sprintf("[green]%s %s[-]", tview.Escape(metric.Key), label))
			t.refresh()
		}()

		return
	}

	template, ok := t.selectedTemplate()
	if !ok {
		return
	}

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
		defer cancel()

		if err := t.source.Mark(ctx, template.Template, marking); err != nil {
			t.setMessage(fmt.Sprintf("[red]mark failed: %v[-]", err))

			return
		}

		label := string(marking)
		if label == "" {
			label = "cleared"
		}

		t.setMessage(fmt.Sprintf("[green]%s/%d marked %s[-]", tview.Escape(template.Partition), template.ID, label))
		t.refresh()
	}()
}

func (t *top) showDetail() {
	if event, ok := t.selectedEvent(); ok {
		t.openDetail(
			fmt.Sprintf(" %s · %s · %s ", event.Kind, event.Source, event.Timestamp.Local().Format(time.RFC3339)),
			eventDetail(event))

		return
	}

	template, ok := t.selectedTemplate()
	if !ok {
		return
	}

	t.openDetail(
		fmt.Sprintf(" %s/%d · size %d · %s ", template.Partition, template.ID, template.Size, markingLabel(template.Marking)),
		template.Template)
}

func (t *top) openDetail(title string, body string) {
	detail := tview.NewTextView().SetWrap(true)
	detail.SetBorder(true).SetTitle(title)
	detail.SetText(body)

	t.pages.AddPage(pageDetail, detail, true, true)
	t.app.SetFocus(detail)
}

func markingLabel(marking detect.Marking) string {
	if marking == "" {
		return "unmarked"
	}

	return string(marking)
}

func (t *top) refreshLoop(ctx context.Context) {
	t.refresh()

	ticker := time.NewTicker(t.opts.Refresh)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			t.refresh()
		}
	}
}

// refresh fetches fresh data and redraws; it runs outside the UI
// goroutine.
func (t *top) refresh() {
	ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
	defer cancel()

	templates, err := t.source.Templates(ctx)
	if err != nil {
		t.setMessage(fmt.Sprintf("[red]refresh failed: %v[-]", err))

		return
	}

	metrics, err := t.source.Metrics(ctx)
	if err != nil {
		t.setMessage(fmt.Sprintf("[red]refresh failed: %v[-]", err))

		return
	}

	t.mu.Lock()
	t.templates = templates
	t.metrics = metrics
	t.fetchedAt = time.Now()
	t.mu.Unlock()

	t.app.QueueUpdateDraw(t.render)
}

// streamLoop follows the server's event feed, reconnecting when the
// server restarts. It runs outside the UI goroutine.
func (t *top) streamLoop(ctx context.Context) {
	// Seed from the persistent log first: the live ring only remembers
	// since the server started. Failing is fine, the stream fills in.
	if history, err := t.source.ListEvents(ctx, eventHistory); err == nil {
		for _, event := range history {
			t.rememberEvent(event)
		}

		t.app.QueueUpdateDraw(t.render)
	}

	for ctx.Err() == nil {
		events := make(chan model.Event, 64)

		done := make(chan struct{})
		go func() {
			defer close(done)

			for event := range events {
				t.appendEvent(event)
			}
		}()

		err := t.source.Events(ctx, eventHistory, events, func() { t.setStreaming(true) })
		close(events)
		<-done

		t.setStreaming(false)

		if ctx.Err() != nil {
			return
		}

		if err != nil {
			t.setMessage(fmt.Sprintf("[red]event stream lost: %v[-]", err))
		}

		select {
		case <-ctx.Done():
			return
		case <-time.After(eventReconnectDelay):
		}
	}
}

// appendEvent adds one event to the ring kept in memory, newest last.
func (t *top) appendEvent(event model.Event) {
	if !t.rememberEvent(event) {
		return
	}

	t.app.QueueUpdateDraw(t.render)
}

// rememberEvent stores an event unless it is already known; it reports
// whether the view changed.
func (t *top) rememberEvent(event model.Event) bool {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.seen == nil {
		t.seen = map[string]bool{}
	}

	if event.ID != "" && t.seen[event.ID] {
		return false
	}

	if event.ID != "" {
		t.seen[event.ID] = true
	}

	t.events = append(t.events, event)
	if len(t.events) > eventHistory {
		for _, dropped := range t.events[:len(t.events)-eventHistory] {
			delete(t.seen, dropped.ID)
		}

		t.events = t.events[len(t.events)-eventHistory:]
	}

	return true
}

func (t *top) setStreaming(streaming bool) {
	t.mu.Lock()
	t.streaming = streaming
	t.mu.Unlock()

	t.app.QueueUpdateDraw(t.render)
}

// setMessage records a transient status message; it runs outside the
// UI goroutine.
func (t *top) setMessage(message string) {
	t.mu.Lock()
	t.message = message
	t.messageAt = time.Now()
	t.mu.Unlock()

	t.app.QueueUpdateDraw(t.render)
}

// render rebuilds the tables and status line from the latest data; it
// must run on the UI goroutine.
func (t *top) render() {
	t.mu.Lock()
	templates := templateRows(t.templates, t.query)
	metrics := metricRows(t.metrics, t.query)
	events := eventRows(t.events, t.query)
	t.visible = templates
	t.visibleEvents = events
	t.visibleMetrics = metrics
	fetchedAt := t.fetchedAt
	streaming := t.streaming
	message := t.message
	if time.Since(t.messageAt) > messageTTL {
		message = ""
	}
	t.mu.Unlock()

	renderEvents(t.eventsTable, events)
	renderTemplates(t.templatesTable, templates)
	renderMetrics(t.metricsTable, metrics)

	feed := "[red]disconnected[-]"
	if streaming {
		feed = "live"
	}

	status := fmt.Sprintf(" %d events (%s) · %d templates · %d series", len(events), feed, len(templates), len(metrics))
	if !fetchedAt.IsZero() {
		status += " · refreshed " + fetchedAt.Format("15:04:05")
	}
	if message != "" {
		status += " · " + message
	}

	t.status.SetText(status)
}

func renderEvents(table *tview.Table, events []model.Event) {
	table.Clear()

	setHeader(table, "TIME", "SEVERITY", "KIND", "SOURCE", "SUMMARY")

	for i, event := range events {
		row := i + 1

		color := severityColor(event.Severity)

		table.SetCell(row, 0, tview.NewTableCell(event.Timestamp.Local().Format("15:04:05")).SetTextColor(color))
		table.SetCell(row, 1, tview.NewTableCell(string(event.Severity)).SetTextColor(color))
		table.SetCell(row, 2, tview.NewTableCell(tview.Escape(event.Kind)).SetTextColor(color))
		table.SetCell(row, 3, tview.NewTableCell(tview.Escape(event.Source)).SetTextColor(color))
		table.SetCell(row, 4, tview.NewTableCell(tview.Escape(event.Summary)).SetTextColor(color).SetMaxWidth(100).SetExpansion(1))
	}

	clampSelection(table, len(events))
}

func severityColor(severity model.Severity) tcell.Color {
	switch severity {
	case model.SeverityCritical:
		return tcell.ColorRed
	case model.SeverityWarning:
		return tcell.ColorYellow
	default:
		return tcell.ColorDefault
	}
}

// eventDetail lays out one event for the detail pane: the summary, the
// signals that triggered it and the changes it correlates with, then
// the raw JSON.
func eventDetail(event model.Event) string {
	var b strings.Builder

	fmt.Fprintf(&b, "%s\n\n", event.Summary)
	fmt.Fprintf(&b, "severity   %s (confidence %.2f)\n", event.Severity, event.Confidence)
	fmt.Fprintf(&b, "service    %s / %s\n", valueOr(event.Environment, "-"), valueOr(event.Service, "-"))
	fmt.Fprintf(&b, "timestamp  %s\n", event.Timestamp.Local().Format(time.RFC3339))

	if len(event.Signals) > 0 {
		b.WriteString("\nsignals\n")

		for _, signal := range event.Signals {
			fmt.Fprintf(&b, "  %-28s score %.2f  %s\n", signal.Kind, signal.Score, signal.Summary)
		}
	}

	if len(event.RelatedChanges) > 0 {
		b.WriteString("\nrelated changes (correlation, not causation)\n")

		for _, related := range event.RelatedChanges {
			fmt.Fprintf(&b, "  %+.0fs  %s %s %s\n", related.OffsetSeconds,
				related.Change.Type, valueOr(related.Change.Version, ""), related.Change.Summary)
		}
	}

	if encoded, err := json.MarshalIndent(event, "", "  "); err == nil {
		fmt.Fprintf(&b, "\njson\n%s\n", encoded)
	}

	return b.String()
}

func valueOr(value string, fallback string) string {
	if value == "" {
		return fallback
	}

	return value
}

func renderTemplates(table *tview.Table, templates []admin.TemplateInfo) {
	table.Clear()

	setHeader(table, "PARTITION", "ID", "SIZE", "MARKING", "TEMPLATE")

	for i, info := range templates {
		row := i + 1

		color := tcell.ColorDefault
		switch info.Marking {
		case detect.MarkingNormal:
			color = tcell.ColorGreen
		case detect.MarkingIgnore:
			color = tcell.ColorGray
		case detect.MarkingSymptomatic:
			color = tcell.ColorRed
		}

		table.SetCell(row, 0, tview.NewTableCell(tview.Escape(info.Partition)).SetTextColor(color))
		table.SetCell(row, 1, tview.NewTableCell(strconv.FormatInt(info.ID, 10)).SetTextColor(color).SetAlign(tview.AlignRight))
		table.SetCell(row, 2, tview.NewTableCell(strconv.FormatInt(info.Size, 10)).SetTextColor(color).SetAlign(tview.AlignRight))
		table.SetCell(row, 3, tview.NewTableCell(string(info.Marking)).SetTextColor(color))
		table.SetCell(row, 4, tview.NewTableCell(tview.Escape(info.Template)).SetTextColor(color).SetMaxWidth(120).SetExpansion(1))
	}

	clampSelection(table, len(templates))
}

func renderMetrics(table *tview.Table, metrics []metricRow) {
	table.Clear()

	setHeader(table, "SERIES", "SAMPLES", "MEAN", "STDDEV", "RECENT", "DEV", "STATE")

	for i, info := range metrics {
		row := i + 1

		color := tcell.ColorDefault
		state := "ready"
		if info.Warmup {
			color = tcell.ColorGray
			state = "warming up"
		} else if info.Deviation >= 3 {
			color = tcell.ColorRed
		}

		if info.Ignored {
			color = tcell.ColorGray
			state = "ignored"
		}

		table.SetCell(row, 0, tview.NewTableCell(tview.Escape(info.Key)).SetTextColor(color).SetMaxWidth(80).SetExpansion(1))
		table.SetCell(row, 1, tview.NewTableCell(strconv.FormatInt(info.Samples, 10)).SetTextColor(color).SetAlign(tview.AlignRight))
		table.SetCell(row, 2, tview.NewTableCell(formatValue(info.Mean)).SetTextColor(color).SetAlign(tview.AlignRight))
		table.SetCell(row, 3, tview.NewTableCell(formatValue(info.StdDev)).SetTextColor(color).SetAlign(tview.AlignRight))
		table.SetCell(row, 4, tview.NewTableCell(formatValue(info.Recent)).SetTextColor(color).SetAlign(tview.AlignRight))
		table.SetCell(row, 5, tview.NewTableCell(formatDeviation(info.Deviation)).SetTextColor(color).SetAlign(tview.AlignRight))
		table.SetCell(row, 6, tview.NewTableCell(state).SetTextColor(color))
	}

	clampSelection(table, len(metrics))
}

func setHeader(table *tview.Table, columns ...string) {
	for col, column := range columns {
		table.SetCell(0, col, tview.NewTableCell(column).
			SetTextColor(tcell.ColorYellow).
			SetAttributes(tcell.AttrBold).
			SetSelectable(false))
	}
}

// clampSelection keeps the selected row within the data rows after a
// rebuild shrank the table.
func clampSelection(table *tview.Table, rows int) {
	if rows == 0 {
		return
	}

	row, _ := table.GetSelection()

	if row < 1 {
		table.Select(1, 0)
	} else if row > rows {
		table.Select(rows, 0)
	}
}

func formatValue(value float64) string {
	return strconv.FormatFloat(value, 'g', 4, 64)
}

func formatDeviation(deviation float64) string {
	switch {
	case math.IsNaN(deviation):
		return "-"
	case math.IsInf(deviation, 1):
		return "inf"
	default:
		return strconv.FormatFloat(deviation, 'f', 1, 64) + "σ"
	}
}
