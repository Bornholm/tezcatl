package tui

import (
	"context"
	"fmt"
	"math"
	"strconv"
	"sync"
	"time"

	"github.com/bornholm/tezcatl/internal/core/admin"
	"github.com/bornholm/tezcatl/internal/core/detect"
	"github.com/gdamore/tcell/v2"
	"github.com/pkg/errors"
	"github.com/rivo/tview"
)

// Source provides the data shown and mutated by the top view.
type Source interface {
	Templates(ctx context.Context) ([]admin.TemplateInfo, error)
	Metrics(ctx context.Context) ([]detect.SeriesInfo, error)
	Mark(ctx context.Context, template string, marking detect.Marking) error
}

type Options struct {
	Target  string
	Version string
	Refresh time.Duration
}

const (
	pageTemplates = "templates"
	pageMetrics   = "metrics"
	pageDetail    = "detail"

	requestTimeout = 10 * time.Second
	messageTTL     = 5 * time.Second
)

type top struct {
	app    *tview.Application
	pages  *tview.Pages
	status *tview.TextView
	filter *tview.InputField

	templatesTable *tview.Table
	metricsTable   *tview.Table

	source Source
	opts   Options

	mu        sync.Mutex
	templates []admin.TemplateInfo
	metrics   []detect.SeriesInfo
	// visible mirrors the rows currently shown by templatesTable, so
	// key handlers can map a selected row back to its template.
	visible   []admin.TemplateInfo
	query     string
	fetchedAt time.Time
	message   string
	messageAt time.Time
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
	fmt.Fprintf(header, " [yellow]1[-] templates  [yellow]2[-] metrics  [yellow]/[-] filter  [yellow]n/i/s/c[-] mark  [yellow]enter[-] detail  [yellow]r[-] refresh  [yellow]q[-] quit")

	t.templatesTable = newTable()
	t.templatesTable.SetSelectedFunc(func(row, col int) { t.showDetail() })
	t.metricsTable = newTable()

	t.pages = tview.NewPages().
		AddPage(pageTemplates, t.templatesTable, true, true).
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
		t.pages.SwitchToPage(pageTemplates)
		t.app.SetFocus(t.templatesTable)
		return nil
	case '2':
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
	if name, _ := t.pages.GetFrontPage(); name == pageMetrics {
		return t.metricsTable
	}

	return t.templatesTable
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

func (t *top) markSelected(marking detect.Marking) {
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
	template, ok := t.selectedTemplate()
	if !ok {
		return
	}

	detail := tview.NewTextView().SetWrap(true)
	detail.SetBorder(true).SetTitle(fmt.Sprintf(" %s/%d — size %d — %s ",
		template.Partition, template.ID, template.Size, markingLabel(template.Marking)))
	detail.SetText(template.Template)

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
	t.visible = templates
	fetchedAt := t.fetchedAt
	message := t.message
	if time.Since(t.messageAt) > messageTTL {
		message = ""
	}
	t.mu.Unlock()

	renderTemplates(t.templatesTable, templates)
	renderMetrics(t.metricsTable, metrics)

	status := fmt.Sprintf(" %d templates · %d series", len(templates), len(metrics))
	if !fetchedAt.IsZero() {
		status += " · refreshed " + fetchedAt.Format("15:04:05")
	}
	if message != "" {
		status += " · " + message
	}

	t.status.SetText(status)
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
