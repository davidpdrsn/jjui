package ai_implement

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

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

const refreshDelay = 500 * time.Millisecond

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
	remove            bool
	plan              bool
	useNix            bool
	inTmux            bool
	removeKey         key.Binding
	planKey           key.Binding
}

type styles struct {
	sourceMarker lipgloss.Style
}

func (a *Operation) IsFocused() bool {
	return true
}

func (a *Operation) Init() tea.Cmd {
	return nil
}

func (a *Operation) Update(msg tea.Msg) tea.Cmd {
	switch msg := msg.(type) {
	case intents.Intent:
		return a.handleIntent(msg)
	case tea.KeyMsg:
		return a.HandleKey(msg)
	}
	return nil
}

func (a *Operation) ViewRect(_ *render.DisplayContext, _ layout.Box) {}

func (a *Operation) HandleKey(msg tea.KeyMsg) tea.Cmd {
	switch {
	case key.Matches(msg, a.keyMap.AceJump):
		return a.handleIntent(intents.StartAceJump{})
	case key.Matches(msg, a.keyMap.Apply, a.keyMap.ForceApply):
		return a.handleIntent(intents.Apply{})
	case key.Matches(msg, a.keyMap.ToggleSelect):
		return a.handleIntent(intents.AiImplementToggleSelect{})
	case key.Matches(msg, a.keyMap.Cancel):
		return a.handleIntent(intents.Cancel{})
	case key.Matches(msg, a.removeKey):
		return a.handleIntent(intents.AiImplementToggleRemove{})
	case key.Matches(msg, a.planKey):
		if !a.remove {
			a.plan = !a.plan
		}
	}
	return nil
}

func (a *Operation) handleIntent(intent intents.Intent) tea.Cmd {
	switch intent.(type) {
	case intents.StartAceJump:
		return common.StartAceJump()
	case intents.Apply:
		if len(a.selectedRevisions.Revisions) == 0 {
			return nil
		}
		return a.applyCommands()
	case intents.AiImplementToggleSelect:
		if a.current == nil {
			return nil
		}
		item := context.SelectedRevision{
			ChangeId: a.current.GetChangeId(),
			CommitId: a.current.CommitId,
		}
		a.context.ToggleCheckedItem(item)
		a.toggleSelectedRevision(a.current)
		return nil
	case intents.AiImplementToggleRemove:
		a.remove = !a.remove
		if a.remove {
			a.plan = false
		}
		return nil
	case intents.Cancel:
		return common.Close
	}
	return nil
}

func (a *Operation) ShortHelp() []key.Binding {
	return []key.Binding{
		a.keyMap.Apply,
		a.keyMap.ToggleSelect,
		a.keyMap.Cancel,
		a.removeKey,
		a.planKey,
		a.keyMap.AceJump,
	}
}

func (a *Operation) FullHelp() [][]key.Binding {
	return [][]key.Binding{a.ShortHelp()}
}

func (a *Operation) SetSelectedRevision(commit *jj.Commit) tea.Cmd {
	a.current = commit
	return nil
}

func (a *Operation) Render(commit *jj.Commit, pos operations.RenderPosition) string {
	if pos != operations.RenderBeforeChangeId {
		return ""
	}
	if !a.selectedRevisions.Contains(commit) {
		return ""
	}
	marker := "<< ai implement >>"
	if a.remove {
		marker = "<< ai remove >>"
	} else if a.plan {
		marker = "<< ai plan >>"
	}
	return a.styles.sourceMarker.Render(marker)
}

func (a *Operation) RenderToDisplayContext(_ *render.DisplayContext, _ *jj.Commit, _ operations.RenderPosition, _ cellbuf.Rectangle, _ cellbuf.Position) int {
	return 0
}

func (a *Operation) DesiredHeight(_ *jj.Commit, _ operations.RenderPosition) int {
	return 0
}

func (a *Operation) Name() string {
	return "ai implement"
}

func delayedRefresh() tea.Cmd {
	return tea.Tick(refreshDelay, func(time.Time) tea.Msg {
		return common.Refresh()
	})
}

func (a *Operation) applyCommands() tea.Cmd {
	var commands []tea.Cmd
	for idx, revision := range a.selectedRevisions.Revisions {
		isLast := idx == len(a.selectedRevisions.Revisions)-1
		var continuations []tea.Cmd
		if !a.remove && !a.plan {
			continuations = append(continuations, delayedRefresh())
		}
		if isLast {
			if a.remove || a.plan {
				continuations = append(continuations, delayedRefresh())
			}
			continuations = append(continuations, common.Close)
		}
		cmd := a.commandForRevision(revision, continuations...)
		if cmd != nil {
			commands = append(commands, cmd)
		}
	}
	return tea.Sequence(commands...)
}

func (a *Operation) commandForRevision(commit *jj.Commit, continuations ...tea.Cmd) tea.Cmd {
	if commit == nil {
		return nil
	}
	if a.remove {
		args := jj.AiImplementRemove(commit.GetChangeId())
		return a.context.RunProgramCommand(jj.AiImplementProgram, args, continuations...)
	}
	args := jj.AiImplementAdd(commit.GetChangeId(), a.useNix, a.plan)
	if a.inTmux {
		if a.plan {
			name := tmuxWindowName(a.context.Location, commit.GetChangeId())
			selectCmd := a.context.RunProgramCommand("tmux", []string{"select-window", "-t", name}, continuations...)
			return a.context.RunProgramCommand("tmux", a.tmuxArgs(commit, args), selectCmd)
		}
		return a.context.RunProgramCommand("tmux", a.tmuxArgs(commit, args), continuations...)
	}
	return a.context.RunProgramCommand(jj.AiImplementProgram, args, continuations...)
}

func (a *Operation) tmuxArgs(commit *jj.Commit, args []string) []string {
	command := strings.Join(append([]string{jj.AiImplementProgram}, args...), " ")
	name := tmuxWindowName(a.context.Location, commit.GetChangeId())
	commandArgs := []string{"new-window", "-d", "-a", "-t", ":", "-n", name}
	if a.context.Location != "" {
		commandArgs = append(commandArgs, "-c", a.context.Location)
	}
	return append(commandArgs, command)
}

func tmuxWindowName(location string, revision string) string {
	dirName := filepath.Base(location)
	if dirName == "." || dirName == string(filepath.Separator) {
		dirName = ""
	}
	if len(revision) > 5 {
		revision = revision[:5]
	}
	if dirName == "" {
		return revision
	}
	return fmt.Sprintf("%s-%s", dirName, revision)
}

func (a *Operation) toggleSelectedRevision(commit *jj.Commit) {
	if commit == nil {
		return
	}
	if a.selectedRevisions.Contains(commit) {
		var kept []*jj.Commit
		for _, revision := range a.selectedRevisions.Revisions {
			if revision.GetChangeId() != commit.GetChangeId() {
				kept = append(kept, revision)
			}
		}
		a.selectedRevisions = jj.NewSelectedRevisions(kept...)
		return
	}
	a.selectedRevisions = jj.NewSelectedRevisions(append(a.selectedRevisions.Revisions, commit)...)
}

func NewOperation(context *context.MainContext, selectedRevisions jj.SelectedRevisions) *Operation {
	styles := styles{
		sourceMarker: common.DefaultPalette.Get("ai_implement source_marker"),
	}
	return &Operation{
		context:           context,
		selectedRevisions: selectedRevisions,
		keyMap:            config.Current.GetKeyMap(),
		styles:            styles,
		removeKey:         key.NewBinding(key.WithKeys("r"), key.WithHelp("r", "remove")),
		planKey:           key.NewBinding(key.WithKeys("p"), key.WithHelp("p", "plan")),
		useNix:            flakeExists(context.Location),
		inTmux:            os.Getenv("TMUX") != "",
	}
}

func flakeExists(location string) bool {
	if location == "" {
		return false
	}
	_, err := os.Stat(filepath.Join(location, "flake.nix"))
	return err == nil || !errors.Is(err, os.ErrNotExist)
}
