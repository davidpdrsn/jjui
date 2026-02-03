package integrate

import (
	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/cellbuf"
	"github.com/idursun/jjui/internal/config"
	"github.com/idursun/jjui/internal/jj"
	"github.com/idursun/jjui/internal/ui/common"
	"github.com/idursun/jjui/internal/ui/context"
	"github.com/idursun/jjui/internal/ui/intents"
	"github.com/idursun/jjui/internal/ui/layout"
	"github.com/idursun/jjui/internal/ui/operations"
	"github.com/idursun/jjui/internal/ui/render"
)

var (
	_ operations.Operation = (*Operation)(nil)
	_ common.Focusable     = (*Operation)(nil)
)

type Operation struct {
	context           *context.MainContext
	selectedRevisions jj.SelectedRevisions
	current           *jj.Commit
	keyMap            config.KeyMappings[key.Binding]
	styles            styles
}

type styles struct {
	sourceMarker lipgloss.Style
}

func (o *Operation) IsFocused() bool {
	return true
}

func (o *Operation) Init() tea.Cmd {
	return nil
}

func (o *Operation) Update(msg tea.Msg) tea.Cmd {
	switch msg := msg.(type) {
	case intents.Intent:
		return o.handleIntent(msg)
	case tea.KeyMsg:
		return o.HandleKey(msg)
	}
	return nil
}

func (o *Operation) ViewRect(_ *render.DisplayContext, _ layout.Box) {}

func (o *Operation) HandleKey(msg tea.KeyMsg) tea.Cmd {
	switch {
	case key.Matches(msg, o.keyMap.AceJump):
		return o.handleIntent(intents.StartAceJump{})
	case key.Matches(msg, o.keyMap.Apply):
		return o.handleIntent(intents.Apply{})
	case key.Matches(msg, o.keyMap.ToggleSelect):
		return o.handleIntent(intents.IntegrateToggleSelect{})
	case key.Matches(msg, o.keyMap.Cancel):
		return o.handleIntent(intents.Cancel{})
	}
	return nil
}

func (o *Operation) handleIntent(intent intents.Intent) tea.Cmd {
	switch intent.(type) {
	case intents.StartAceJump:
		return common.StartAceJump()
	case intents.Apply:
		if len(o.selectedRevisions.Revisions) == 0 {
			return nil
		}
		return o.context.RunCommand(jj.Integrate(o.selectedRevisions), common.Refresh, common.Close)
	case intents.IntegrateToggleSelect:
		if o.current == nil {
			return nil
		}
		item := context.SelectedRevision{
			ChangeId: o.current.GetChangeId(),
			CommitId: o.current.CommitId,
		}
		o.context.ToggleCheckedItem(item)
		o.toggleSelectedRevision(o.current)
		return nil
	case intents.Cancel:
		return common.Close
	}
	return nil
}

func (o *Operation) ShortHelp() []key.Binding {
	return []key.Binding{
		o.keyMap.Apply,
		o.keyMap.ToggleSelect,
		o.keyMap.Cancel,
		o.keyMap.AceJump,
	}
}

func (o *Operation) FullHelp() [][]key.Binding {
	return [][]key.Binding{o.ShortHelp()}
}

func (o *Operation) SetSelectedRevision(commit *jj.Commit) tea.Cmd {
	o.current = commit
	return nil
}

func (o *Operation) Render(commit *jj.Commit, pos operations.RenderPosition) string {
	if pos != operations.RenderBeforeChangeId {
		return ""
	}
	if !o.selectedRevisions.Contains(commit) {
		return ""
	}
	return o.styles.sourceMarker.Render("<< integrate >>")
}

func (o *Operation) RenderToDisplayContext(_ *render.DisplayContext, _ *jj.Commit, _ operations.RenderPosition, _ cellbuf.Rectangle, _ cellbuf.Position) int {
	return 0
}

func (o *Operation) DesiredHeight(_ *jj.Commit, _ operations.RenderPosition) int {
	return 0
}

func (o *Operation) Name() string {
	return "integrate"
}

func (o *Operation) toggleSelectedRevision(commit *jj.Commit) {
	if commit == nil {
		return
	}
	if o.selectedRevisions.Contains(commit) {
		var kept []*jj.Commit
		for _, revision := range o.selectedRevisions.Revisions {
			if revision.GetChangeId() != commit.GetChangeId() {
				kept = append(kept, revision)
			}
		}
		o.selectedRevisions = jj.NewSelectedRevisions(kept...)
		return
	}
	o.selectedRevisions = jj.NewSelectedRevisions(append(o.selectedRevisions.Revisions, commit)...)
}

func NewOperation(context *context.MainContext, selectedRevisions jj.SelectedRevisions) *Operation {
	styles := styles{
		sourceMarker: common.DefaultPalette.Get("integrate source_marker"),
	}
	return &Operation{
		context:           context,
		selectedRevisions: selectedRevisions,
		keyMap:            config.Current.GetKeyMap(),
		styles:            styles,
	}
}
