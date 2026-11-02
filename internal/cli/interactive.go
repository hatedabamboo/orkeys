package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/atotto/clipboard"
	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/huh"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/spf13/cobra"

	"github.com/hatedabamboo/orkeys/internal/api"
	"github.com/hatedabamboo/orkeys/internal/values"
)

// Interactive mode is one inline Bubble Tea program. It owns a region of the
// console and redraws it in place. The region grows to the tallest screen so
// far (capped at the terminal height) and never shrinks, so screens do not
// jump. On exit the region is erased.

var (
	accent    = lipgloss.AdaptiveColor{Light: "#5A3FD9", Dark: "#9D8CFF"}
	headStyle = lipgloss.NewStyle().Bold(true).Foreground(accent)
	okStyle   = lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "#1A7F37", Dark: "#5FD787"})
	errStyle  = lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "#C0262D", Dark: "#FF6B6B"})
	warnStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.AdaptiveColor{Light: "#9A6700", Dark: "#F2CC60"})
	dimStyle  = lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "#888888", Dark: "#777777"})
	boldStyle = lipgloss.NewStyle().Bold(true)
)

// keymap: esc cancels a prompt. ctrl+c is handled by the model and quits.
var keymap = func() *huh.KeyMap {
	km := huh.NewDefaultKeyMap()
	km.Quit = key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "back"))
	return km
}()

// chrome is the number of lines around the body: header, blank, blank, footer.
const chrome = 4

func (a *app) interactiveCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "interactive",
		Aliases: []string{"i"},
		Short:   "Manage keys interactively in the console",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return a.runInteractive(cmd.Context())
		},
	}
}

func (a *app) runInteractive(ctx context.Context) error {
	if !isTTY() {
		return errors.New("interactive mode needs a terminal")
	}
	c, err := a.client()
	if err != nil {
		return err
	}
	sp := spinner.New(spinner.WithSpinner(spinner.Dot), spinner.WithStyle(lipgloss.NewStyle().Foreground(accent)))
	m := &imodel{a: a, c: c, ctx: ctx, spinner: sp, json: a.output == "json"}
	_, err = tea.NewProgram(m, tea.WithContext(ctx)).Run()
	if errors.Is(err, tea.ErrProgramKilled) && ctx.Err() != nil {
		return nil //nolint:nilerr // killed by SIGINT/SIGTERM: a normal exit
	}
	return err
}

type screen int

const (
	scrBusy screen = iota
	scrForm
	scrText
)

type resultMsg struct {
	v    any
	err  error
	then func(*imodel, any) tea.Cmd
}

type imodel struct {
	a   *app
	c   *api.Client
	ctx context.Context

	keys []api.Key

	scr      screen
	busyText string
	spinner  spinner.Model

	form        *huh.Form
	onDone      func(*imodel) tea.Cmd
	cancelQuits bool

	text       viewport.Model
	textTitle  string
	textHelp   string
	textKeys   map[string]func(*imodel) tea.Cmd
	textRender func(format string) string
	json       bool // text screens show JSON instead of tables

	status    string
	statusErr bool

	width, height int
	region        int // lines the app occupies; grows, never shrinks
	quitting      bool
}

// ---- plumbing -------------------------------------------------------------

func (m *imodel) Init() tea.Cmd {
	return m.loadKeys("Loading keys…", func(m *imodel) tea.Cmd { return m.menu() })
}

func (m *imodel) setOK(format string, args ...any) {
	m.status, m.statusErr = "✓ "+fmt.Sprintf(format, args...), false
}

func (m *imodel) setErr(err error) { m.status, m.statusErr = "✗ "+err.Error(), true }

// maxBody is the tallest body that fits the terminal.
func (m *imodel) maxBody() int {
	if m.height <= 0 {
		return 20
	}
	return max(3, m.height-1-chrome)
}

func (m *imodel) formWidth() int {
	if m.width <= 0 {
		return 100
	}
	return min(m.width, 120)
}

// do runs work in the background with a spinner, then calls then on success.
// On error it shows the error and returns to the menu.
func (m *imodel) do(busy string, work func(context.Context) (any, error), then func(*imodel, any) tea.Cmd) tea.Cmd {
	m.scr, m.busyText, m.form = scrBusy, busy, nil
	ctx := m.ctx
	return tea.Batch(m.spinner.Tick, func() tea.Msg {
		v, err := work(ctx)
		return resultMsg{v: v, err: err, then: then}
	})
}

func (m *imodel) loadKeys(busy string, then func(*imodel) tea.Cmd) tea.Cmd {
	opts := api.ListOptions{IncludeDisabled: true, WorkspaceID: m.a.workspace}
	return m.do(busy,
		func(ctx context.Context) (any, error) { return m.c.ListAll(ctx, opts) },
		func(m *imodel, v any) tea.Cmd {
			m.keys = v.([]api.Key)
			sort.SliceStable(m.keys, func(i, j int) bool { return m.keys[i].CreatedAt > m.keys[j].CreatedAt })
			return then(m)
		})
}

func (m *imodel) openForm(onDone func(*imodel) tea.Cmd, fields ...huh.Field) tea.Cmd {
	m.form = huh.NewForm(huh.NewGroup(fields...)).
		WithKeyMap(keymap).
		WithShowHelp(true).
		WithWidth(m.formWidth())
	m.onDone, m.cancelQuits, m.scr = onDone, false, scrForm
	return m.form.Init()
}

// format is the output format for text screens.
func (m *imodel) format() string {
	switch {
	case m.json:
		return "json"
	case m.a.output == "wide":
		return "wide"
	}
	return "table"
}

// showText shows read-only content in a scrollable view sized to fit.
// render builds the content for a format; j switches between table and JSON.
func (m *imodel) showText(title string, render func(format string) string, help string, keys map[string]func(*imodel) tea.Cmd) tea.Cmd {
	m.textRender = render
	content := strings.TrimRight(render(m.format()), "\n")
	if m.width > 0 {
		lines := strings.Split(content, "\n")
		for i, l := range lines {
			lines[i] = ansi.Truncate(l, m.width, "…")
		}
		content = strings.Join(lines, "\n")
	}
	h := min(strings.Count(content, "\n")+1, m.maxBody()-1) // -1: title line
	m.text = viewport.New(max(m.width, 20), max(1, h))
	m.text.SetContent(content)
	m.textTitle, m.textHelp, m.textKeys = title, help, keys
	m.scr, m.form = scrText, nil
	return nil
}

func (m *imodel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		if m.region > m.height-1 {
			m.region = max(0, m.height-1)
		}
		if m.scr == scrText {
			m.text.Width = msg.Width
			m.text.Height = max(1, min(m.text.TotalLineCount(), m.maxBody()-1))
		}
	case tea.KeyMsg:
		if msg.String() == "ctrl+c" {
			m.quitting = true
			return m, tea.Quit
		}
	case resultMsg:
		if msg.err != nil {
			m.setErr(msg.err)
			return m, m.menu()
		}
		return m, msg.then(m, msg.v)
	case spinner.TickMsg:
		if m.scr != scrBusy {
			return m, nil
		}
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		return m, cmd
	}

	switch m.scr {
	case scrForm:
		fm, cmd := m.form.Update(msg)
		if f, ok := fm.(*huh.Form); ok {
			m.form = f
		}
		switch m.form.State {
		case huh.StateCompleted:
			return m, m.onDone(m)
		case huh.StateAborted:
			if m.cancelQuits {
				m.quitting = true
				return m, tea.Quit
			}
			m.status, m.statusErr = "Cancelled", false
			return m, m.menu()
		}
		return m, cmd

	case scrText:
		if km, ok := msg.(tea.KeyMsg); ok {
			switch km.String() {
			case "esc", "enter", "q", "backspace":
				return m, m.menu()
			case "j":
				m.json = !m.json
				return m, m.showText(m.textTitle, m.textRender, m.textHelp, m.textKeys)
			}
			if fn := m.textKeys[km.String()]; fn != nil {
				return m, fn(m)
			}
		}
		var cmd tea.Cmd
		m.text, cmd = m.text.Update(msg)
		return m, cmd
	}
	return m, nil
}

func (m *imodel) View() string {
	if m.quitting {
		return "" // erase the region
	}

	disabled := 0
	for _, k := range m.keys {
		if k.Disabled {
			disabled++
		}
	}
	header := headStyle.Render("OpenRouter keys") +
		dimStyle.Render(fmt.Sprintf(" · %d active, %d disabled", len(m.keys)-disabled, disabled))
	if m.a.workspace != "" {
		header += dimStyle.Render(" · workspace " + m.a.workspace)
	}

	var body, footer string
	status := ""
	if m.status != "" {
		if m.statusErr {
			status = errStyle.Render(m.status)
		} else {
			status = okStyle.Render(m.status)
		}
	}

	switch m.scr {
	case scrBusy:
		body = m.spinner.View() + " " + m.busyText
		footer = status
	case scrForm:
		body = strings.TrimRight(m.form.View(), "\n")
		footer = status
	case scrText:
		body = boldStyle.Render(m.textTitle) + "\n" + m.text.View()
		mode := "j json"
		if m.json {
			mode = "j table"
		}
		help := m.textHelp + " • " + mode
		if m.text.TotalLineCount() > m.text.Height {
			help = fmt.Sprintf("↑/↓ pgup/pgdn scroll (%d%%) • %s", int(m.text.ScrollPercent()*100), help)
		}
		footer = dimStyle.Render(help)
		if status != "" {
			footer += "  " + status
		}
	}

	if m.width > 0 {
		header = ansi.Truncate(header, m.width, "…")
		footer = ansi.Truncate(footer, m.width, "…")
	}

	limit := 1000
	if m.height > 0 {
		limit = max(chrome+1, m.height-1)
	}
	// Guard: a body taller than the region would scroll the terminal and
	// break in-place redraws. Drop lines from the top if that happens.
	if lines := strings.Split(body, "\n"); len(lines) > limit-chrome {
		body = strings.Join(lines[len(lines)-(limit-chrome):], "\n")
	}
	n := strings.Count(body, "\n") + 1 + chrome
	if n > m.region {
		m.region = min(n, limit)
	}
	pad := max(0, m.region-n)
	// Padding goes above the footer, so the footer stays on the last line.
	return header + "\n\n" + body + strings.Repeat("\n", pad) + "\n\n" + footer
}

// ---- screens --------------------------------------------------------------

func (m *imodel) menu() tea.Cmd {
	var action string
	cmd := m.openForm(func(m *imodel) tea.Cmd {
		m.status = ""
		switch action {
		case "list":
			return m.list()
		case "show":
			return m.show()
		case "create":
			return m.create()
		case "edit":
			return m.edit()
		case "toggle":
			return m.toggle()
		case "delete":
			return m.delete()
		case "reload":
			return m.loadKeys("Loading keys…", func(m *imodel) tea.Cmd {
				m.setOK("Loaded %d keys", len(m.keys))
				return m.menu()
			})
		}
		m.quitting = true
		return tea.Quit
	}, huh.NewSelect[string]().
		Title("What do you want to do?").
		Options(
			huh.NewOption("List keys", "list"),
			huh.NewOption("Show a key", "show"),
			huh.NewOption("Create a key", "create"),
			huh.NewOption("Edit a key", "edit"),
			huh.NewOption("Enable or disable a key", "toggle"),
			huh.NewOption("Delete keys", "delete"),
			huh.NewOption("Reload", "reload"),
			huh.NewOption("Quit", "quit"),
		).
		Value(&action))
	m.cancelQuits = true
	return cmd
}

func (m *imodel) keyOptions() []huh.Option[string] {
	width := 4
	for _, k := range m.keys {
		width = max(width, min(40, len(k.Name)))
	}
	opts := make([]huh.Option[string], len(m.keys))
	for i, k := range m.keys {
		name := k.Name
		if len(name) > 40 {
			name = name[:39] + "…"
		}
		label := fmt.Sprintf("%-*s  %-8s  %s  limit %s", width, name, k.Status(), values.ShortHash(k.Hash), values.USDPtr(k.Limit))
		opts[i] = huh.NewOption(label, k.Hash)
	}
	return opts
}

// pickerHeight fits all keys, or as many as the terminal allows.
// The height counts the title and description. The form adds a blank line,
// a help line and sometimes an error line under the field.
func (m *imodel) pickerHeight() int {
	return max(5, min(len(m.keys)+2, m.maxBody()-3))
}

func (m *imodel) byHash(h string) api.Key {
	for _, k := range m.keys {
		if k.Hash == h {
			return k
		}
	}
	return api.Key{Hash: h, Name: values.ShortHash(h)}
}

func (m *imodel) replaceKey(k api.Key) {
	for i := range m.keys {
		if m.keys[i].Hash == k.Hash {
			m.keys[i] = k
		}
	}
}

func (m *imodel) pickKey(title string, then func(*imodel, api.Key) tea.Cmd) tea.Cmd {
	if len(m.keys) == 0 {
		m.setErr(errors.New("no keys"))
		return m.menu()
	}
	var hash string
	return m.openForm(func(m *imodel) tea.Cmd { return then(m, m.byHash(hash)) },
		huh.NewSelect[string]().
			Title(title).
			Description("/ to filter").
			Options(m.keyOptions()...).
			Height(m.pickerHeight()).
			Value(&hash))
}

func (m *imodel) copyKey(s, what string) func(*imodel) tea.Cmd {
	return func(m *imodel) tea.Cmd {
		if err := clipboard.WriteAll(s); err != nil {
			m.setErr(fmt.Errorf("copy %s: %w", what, err))
		} else {
			m.setOK("Copied %s", what)
		}
		return nil
	}
}

func (m *imodel) list() tea.Cmd {
	keys := m.keys
	return m.showText(fmt.Sprintf("Keys (%d)", len(keys)), func(format string) string {
		if len(keys) == 0 && format != "json" {
			return dimStyle.Render("No keys.")
		}
		var buf bytes.Buffer
		if err := writeKeysAs(&buf, keys, format); err != nil {
			return errStyle.Render(err.Error())
		}
		return buf.String()
	}, "esc back", nil)
}

func (m *imodel) show() tea.Cmd {
	return m.pickKey("Show which key?", func(m *imodel, k api.Key) tea.Cmd {
		return m.do("Loading "+k.Name+"…",
			func(ctx context.Context) (any, error) { return m.c.Get(ctx, k.Hash) },
			func(m *imodel, v any) tea.Cmd {
				fresh := v.(*api.Key)
				m.replaceKey(*fresh)
				render := func(format string) string {
					var buf bytes.Buffer
					_ = writeKeyAs(&buf, *fresh, format)
					return buf.String()
				}
				return m.showText(fresh.Name, render, "y copy hash • esc back",
					map[string]func(*imodel) tea.Cmd{"y": m.copyKey(fresh.Hash, "hash")})
			})
	})
}

func (m *imodel) create() tea.Cmd {
	var name, limit, expires string
	reset := "none"
	var byok bool

	return m.openForm(func(m *imodel) tea.Cmd {
		req := api.CreateRequest{Name: strings.TrimSpace(name), WorkspaceID: m.a.workspace}
		l, _ := values.ParseLimit(limit)
		req.Limit = l.Ptr()
		r, _ := values.ParseLimitReset(reset)
		req.LimitReset = r.Ptr()
		if byok {
			req.IncludeBYOKInLimit = &byok
		}
		exp, err := values.ParseExpiry(expires, time.Now())
		if err != nil {
			m.setErr(err)
			return m.menu()
		}
		req.ExpiresAt = exp

		return m.do("Creating "+req.Name+"…",
			func(ctx context.Context) (any, error) { return m.c.Create(ctx, req) },
			func(m *imodel, v any) tea.Cmd {
				resp := v.(*api.CreateResponse)
				m.keys = append([]api.Key{resp.Data}, m.keys...)
				render := func(format string) string {
					var buf bytes.Buffer
					if format == "json" {
						_ = printJSON(&buf, resp)
					} else {
						buf.WriteString("Key: " + boldStyle.Render(resp.Key) + "\n")
						_ = writeKeyAs(&buf, resp.Data, format)
					}
					buf.WriteString("\n" + warnStyle.Render("Save this key now. OpenRouter does not show it again."))
					return buf.String()
				}
				return m.showText("Created "+resp.Data.Name, render, "c copy key • esc back",
					map[string]func(*imodel) tea.Cmd{"c": m.copyKey(resp.Key, "key")})
			})
	},
		huh.NewInput().Title("Name").Value(&name).Validate(validateName),
		huh.NewInput().Title("Limit (USD)").Description("empty = no limit").Value(&limit).Validate(validateLimit),
		huh.NewSelect[string]().Title("Limit reset").Options(huh.NewOptions(values.LimitResets...)...).Inline(true).Value(&reset),
		huh.NewConfirm().Title("Count BYOK usage toward the limit?").Affirmative("Yes").Negative("No").Value(&byok),
		huh.NewInput().Title("Expires").Description("30d, 2w, 12h, 2027-12-31 or RFC 3339; empty = never").Value(&expires).
			Validate(func(v string) error { _, err := values.ParseExpiry(v, time.Now()); return err }),
	)
}

func (m *imodel) edit() tea.Cmd {
	return m.pickKey("Edit which key?", func(m *imodel, k api.Key) tea.Cmd {
		name := k.Name
		limit := ""
		if k.Limit != nil {
			limit = strconv.FormatFloat(*k.Limit, 'f', -1, 64)
		}
		reset := "none"
		if k.LimitReset != nil && *k.LimitReset != "" {
			reset = *k.LimitReset
		}
		byok, disabled := k.IncludeBYOKInLimit, k.Disabled

		return m.openForm(func(m *imodel) tea.Cmd {
			req, err := diffUpdate(k, strings.TrimSpace(name), limit, reset, byok, disabled)
			if err != nil {
				m.setErr(err)
				return m.menu()
			}
			if req.Empty() {
				m.status, m.statusErr = "No changes", false
				return m.menu()
			}
			return m.do("Saving "+k.Name+"…",
				func(ctx context.Context) (any, error) { return m.c.Update(ctx, k.Hash, req) },
				func(m *imodel, v any) tea.Cmd {
					u := v.(*api.Key)
					m.replaceKey(*u)
					m.setOK("Updated %s", u.Name)
					return m.menu()
				})
		},
			huh.NewNote().Title("Edit "+k.Name).Description(values.ShortHash(k.Hash)),
			huh.NewInput().Title("Name").Value(&name).Validate(validateName),
			huh.NewInput().Title("Limit (USD)").Description("empty = no limit").Value(&limit).Validate(validateLimit),
			huh.NewSelect[string]().Title("Limit reset").Options(huh.NewOptions(values.LimitResets...)...).Inline(true).Value(&reset),
			huh.NewConfirm().Title("Count BYOK usage toward the limit?").Affirmative("Yes").Negative("No").Value(&byok),
			huh.NewConfirm().Title("Disabled?").Affirmative("Yes").Negative("No").Value(&disabled),
		)
	})
}

func (m *imodel) toggle() tea.Cmd {
	return m.pickKey("Enable or disable which key?", func(m *imodel, k api.Key) tea.Cmd {
		dis := !k.Disabled
		verb, done := "Enable", "Enabled"
		if dis {
			verb, done = "Disable", "Disabled"
		}
		yes := true
		return m.openForm(func(m *imodel) tea.Cmd {
			if !yes {
				m.status, m.statusErr = "Cancelled", false
				return m.menu()
			}
			return m.do(verb+" "+k.Name+"…",
				func(ctx context.Context) (any, error) {
					return m.c.Update(ctx, k.Hash, api.UpdateRequest{Disabled: &dis})
				},
				func(m *imodel, v any) tea.Cmd {
					m.replaceKey(*v.(*api.Key))
					m.setOK("%s %s", done, k.Name)
					return m.menu()
				})
		}, huh.NewConfirm().Title(fmt.Sprintf("%s %s?", verb, k.Name)).Affirmative("Yes").Negative("No").Value(&yes))
	})
}

func (m *imodel) delete() tea.Cmd {
	if len(m.keys) == 0 {
		m.setErr(errors.New("no keys"))
		return m.menu()
	}
	var hashes []string
	return m.openForm(func(m *imodel) tea.Cmd {
		if len(hashes) == 0 {
			m.status, m.statusErr = "Nothing selected", false
			return m.menu()
		}
		var list strings.Builder
		for _, h := range hashes {
			fmt.Fprintf(&list, "%s  %s\n", values.ShortHash(h), m.byHash(h).Name)
		}
		yes := false
		return m.openForm(func(m *imodel) tea.Cmd {
			if !yes {
				m.status, m.statusErr = "Cancelled", false
				return m.menu()
			}
			return m.do(fmt.Sprintf("Deleting %d key(s)…", len(hashes)),
				func(ctx context.Context) (any, error) {
					var done []string
					var errs []error
					for _, h := range hashes {
						if err := m.c.Delete(ctx, h); err != nil {
							errs = append(errs, fmt.Errorf("%s: %w", values.ShortHash(h), err))
							continue
						}
						done = append(done, h)
					}
					return deleteResult{done, errors.Join(errs...)}, nil
				},
				func(m *imodel, v any) tea.Cmd {
					r := v.(deleteResult)
					gone := map[string]bool{}
					for _, h := range r.done {
						gone[h] = true
					}
					kept := m.keys[:0]
					for _, k := range m.keys {
						if !gone[k.Hash] {
							kept = append(kept, k)
						}
					}
					m.keys = kept
					if r.err != nil {
						m.setErr(fmt.Errorf("deleted %d of %d: %w", len(r.done), len(hashes), r.err))
					} else {
						m.setOK("Deleted %d key(s)", len(r.done))
					}
					return m.menu()
				})
		},
			huh.NewNote().Title(fmt.Sprintf("Keys to delete (%d)", len(hashes))).Description(strings.TrimRight(list.String(), "\n")),
			huh.NewConfirm().
				Title(fmt.Sprintf("Delete %d key(s)? Apps that use them stop working. This cannot be undone.", len(hashes))).
				Affirmative("Delete").Negative("Cancel").Value(&yes))
	},
		huh.NewMultiSelect[string]().
			Title("Delete which keys?").
			Description("x select • / filter • enter confirm").
			Options(m.keyOptions()...).
			Height(m.pickerHeight()).
			Filterable(true).
			Value(&hashes))
}

type deleteResult struct {
	done []string
	err  error
}

// ---- validation -----------------------------------------------------------

func validateName(v string) error {
	v = strings.TrimSpace(v)
	if v == "" {
		return errors.New("name is required")
	}
	if len(v) > 256 {
		return errors.New("name is longer than 256 characters")
	}
	return nil
}

func validateLimit(v string) error {
	_, err := values.ParseLimit(v)
	return err
}

// diffUpdate returns a request that holds only the fields that changed.
func diffUpdate(o api.Key, name, limitIn, resetIn string, byok, disabled bool) (api.UpdateRequest, error) {
	var req api.UpdateRequest
	if name != o.Name {
		req.Name = &name
	}
	limit, err := values.ParseLimit(limitIn)
	if err != nil {
		return req, err
	}
	if limit.Valid != (o.Limit != nil) || (limit.Valid && limit.V != *o.Limit) {
		req.Limit = limit
	}
	reset, err := values.ParseLimitReset(resetIn)
	if err != nil {
		return req, err
	}
	if values.Str(reset.Ptr()) != values.Str(o.LimitReset) {
		req.LimitReset = reset
	}
	if byok != o.IncludeBYOKInLimit {
		req.IncludeBYOKInLimit = &byok
	}
	if disabled != o.Disabled {
		req.Disabled = &disabled
	}
	return req, nil
}
