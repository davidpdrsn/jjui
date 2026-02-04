package workspace

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/atotto/clipboard"
	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/cellbuf"
	"github.com/idursun/jjui/internal/config"
	"github.com/idursun/jjui/internal/jj"
	"github.com/idursun/jjui/internal/ui/common"
	"github.com/idursun/jjui/internal/ui/confirmation"
	"github.com/idursun/jjui/internal/ui/context"
	"github.com/idursun/jjui/internal/ui/input"
	"github.com/idursun/jjui/internal/ui/layout"
	"github.com/idursun/jjui/internal/ui/operations"
	"github.com/idursun/jjui/internal/ui/render"
)

var (
	_ operations.Operation = (*Operation)(nil)
	_ common.Editable      = (*Operation)(nil)
	_ common.Overlay       = (*Operation)(nil)
	_ common.Focusable     = (*Operation)(nil)
)

type Operation struct {
	context           *context.MainContext
	selectedRevisions jj.SelectedRevisions
	current           *jj.Commit
	input             *input.Model
	confirmation      *confirmation.Model
	pendingDeletes    []deleteRequest
	activeDelete      *deleteRequest
	pendingForget     int
	keyMap            config.KeyMappings[key.Binding]
	styles            styles
	forget            bool
	forgetKey         key.Binding
}

type styles struct {
	sourceMarker lipgloss.Style
}

type deleteRequest struct {
	name string
	path string
}

type forgetCompletedMsg struct {
	name string
	path string
}

type deleteDecisionMsg struct {
	delete bool
}

type workspaceEntry struct {
	name     string
	changeID string
	commitID string
}

func (o *Operation) Init() tea.Cmd {
	return nil
}

func (o *Operation) Update(msg tea.Msg) tea.Cmd {
	if o.input != nil {
		switch msg := msg.(type) {
		case input.SelectedMsg:
			name := strings.TrimSpace(msg.Value)
			o.input = nil
			if name == "" {
				return common.Close
			}
			return o.applyWorkspace(name)
		case input.CancelledMsg:
			o.input = nil
			return common.Close
		}
		return o.input.Update(msg)
	}

	switch msg := msg.(type) {
	case deleteDecisionMsg:
		return o.handleDeleteChoice(msg.delete)
	case forgetCompletedMsg:
		o.pendingForget--
		o.pendingDeletes = append(o.pendingDeletes, deleteRequest{name: msg.name, path: msg.path})
		return o.showNextDeletePrompt()
	case tea.KeyMsg:
		if o.confirmation != nil {
			return o.confirmation.Update(msg)
		}
		return o.HandleKey(msg)
	}
	return nil
}

func (o *Operation) ViewRect(dl *render.DisplayContext, box layout.Box) {
	if o.input != nil {
		o.input.ViewRect(dl, box)
		return
	}
	if o.confirmation != nil {
		v := o.confirmation.View()
		w, h := lipgloss.Size(v)
		pw, ph := box.R.Dx(), box.R.Dy()
		sx := box.R.Min.X + max((pw-w)/2, 0)
		sy := box.R.Min.Y + max((ph-h)/2, 0)
		frame := cellbuf.Rect(sx, sy, w, h)
		o.confirmation.ViewRect(dl, layout.Box{R: frame})
	}
}

func (o *Operation) IsEditing() bool {
	return o.input != nil
}

func (o *Operation) IsOverlay() bool {
	return o.input != nil || o.confirmation != nil
}

func (o *Operation) IsFocused() bool {
	return true
}

func (o *Operation) HandleKey(msg tea.KeyMsg) tea.Cmd {
	switch {
	case key.Matches(msg, o.keyMap.Apply):
		return o.handleApply()
	case key.Matches(msg, o.keyMap.ToggleSelect):
		return o.toggleSelection()
	case key.Matches(msg, o.keyMap.Cancel):
		return common.Close
	case key.Matches(msg, o.forgetKey):
		o.forget = !o.forget
		return nil
	case key.Matches(msg, o.keyMap.AceJump):
		return common.StartAceJump()
	}
	return nil
}

func (o *Operation) Render(commit *jj.Commit, pos operations.RenderPosition) string {
	if pos != operations.RenderBeforeChangeId {
		return ""
	}
	if !o.selectedRevisions.Contains(commit) {
		return ""
	}
	marker := "<< workspace >>"
	if o.forget {
		marker = "<< forget >>"
	}
	return o.styles.sourceMarker.Render(marker)
}

func (o *Operation) RenderToDisplayContext(_ *render.DisplayContext, _ *jj.Commit, _ operations.RenderPosition, _ cellbuf.Rectangle, _ cellbuf.Position) int {
	return 0
}

func (o *Operation) DesiredHeight(_ *jj.Commit, _ operations.RenderPosition) int {
	return 0
}

func (o *Operation) Name() string {
	return "workspace"
}

func (o *Operation) ShortHelp() []key.Binding {
	if o.confirmation != nil {
		return o.confirmation.ShortHelp()
	}
	return []key.Binding{
		o.keyMap.Apply,
		o.keyMap.ToggleSelect,
		o.keyMap.Cancel,
		o.forgetKey,
		o.keyMap.AceJump,
	}
}

func (o *Operation) FullHelp() [][]key.Binding {
	if o.confirmation != nil {
		return [][]key.Binding{o.confirmation.ShortHelp()}
	}
	return [][]key.Binding{o.ShortHelp()}
}

func (o *Operation) SetSelectedRevision(commit *jj.Commit) tea.Cmd {
	o.current = commit
	return nil
}

func (o *Operation) toggleSelection() tea.Cmd {
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
}

func (o *Operation) handleApply() tea.Cmd {
	if len(o.selectedRevisions.Revisions) == 0 {
		return nil
	}
	if o.forget {
		return o.applyForget()
	}
	o.input = input.NewWithTitle("New workspace", "Name: ")
	return o.input.Init()
}

func (o *Operation) applyWorkspace(name string) tea.Cmd {
	revision := o.selectedRevisions.Last()
	if revision == "" {
		return common.Close
	}
	workspacePath, err := workspacePath(o.context.Location, name)
	if err != nil {
		return func() tea.Msg {
			return common.CommandCompletedMsg{Err: err}
		}
	}
	return o.context.RunCommand(
		jj.WorkspaceAdd(workspacePath, revision, name),
		o.copyWorkspacePathCmd(workspacePath),
		common.Refresh,
		common.Close,
	)
}

func (o *Operation) applyForget() tea.Cmd {
	entries, err := o.workspaceLookup()
	if err != nil {
		return func() tea.Msg {
			return common.CommandCompletedMsg{Err: err}
		}
	}
	o.pendingForget = len(o.selectedRevisions.Revisions)
	o.pendingDeletes = nil
	o.activeDelete = nil
	var commands []tea.Cmd
	for _, revision := range o.selectedRevisions.Revisions {
		name, ok := matchWorkspaceName(entries, revision)
		if !ok {
			return func() tea.Msg {
				return common.CommandCompletedMsg{Err: fmt.Errorf("workspace not found for revision %s", revision.GetChangeId())}
			}
		}
		if name == "default" {
			return func() tea.Msg {
				return common.CommandCompletedMsg{Err: errors.New("cannot forget the default workspace")}
			}
		}
		workspaceDir, err := o.workspaceDirForName(name)
		if err != nil {
			return func() tea.Msg {
				return common.CommandCompletedMsg{Err: err}
			}
		}
		snapshotCmd := o.snapshotWorkspace(workspaceDir)
		forgetCmd := o.context.RunCommand(jj.WorkspaceForget(name), o.forgetCompletedCmd(name, workspaceDir))
		commands = append(commands, tea.Sequence(snapshotCmd, forgetCmd))
	}
	return tea.Sequence(commands...)
}

func (o *Operation) workspaceLookup() ([]workspaceEntry, error) {
	output, err := o.context.RunCommandImmediate(jj.WorkspaceList())
	if err != nil {
		return nil, err
	}
	lines := strings.Split(strings.TrimSpace(string(output)), "\n")
	var entries []workspaceEntry
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, ":", 2)
		if len(parts) != 2 {
			continue
		}
		name := strings.TrimSpace(parts[0])
		fields := strings.Fields(parts[1])
		if len(fields) < 2 || name == "" {
			continue
		}
		entries = append(entries, workspaceEntry{
			name:     name,
			changeID: fields[0],
			commitID: fields[1],
		})
	}
	return entries, nil
}

func matchWorkspaceName(entries []workspaceEntry, revision *jj.Commit) (string, bool) {
	if revision == nil {
		return "", false
	}
	changeID := revision.GetChangeId()
	commitID := revision.CommitId
	if revision.IsWorkingCopy {
		for _, entry := range entries {
			if entry.name == "default" {
				return entry.name, true
			}
		}
	}
	for _, entry := range entries {
		if entry.changeID == changeID || entry.commitID == commitID {
			return entry.name, true
		}
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.changeID, changeID) || strings.HasPrefix(entry.commitID, commitID) {
			return entry.name, true
		}
	}
	return "", false
}

func (o *Operation) forgetCompletedCmd(name string, path string) tea.Cmd {
	return func() tea.Msg {
		return forgetCompletedMsg{name: name, path: path}
	}
}

func (o *Operation) showNextDeletePrompt() tea.Cmd {
	if o.activeDelete != nil || len(o.pendingDeletes) == 0 {
		return o.finalizeIfDone()
	}
	next := o.pendingDeletes[0]
	o.pendingDeletes = o.pendingDeletes[1:]
	o.activeDelete = &next
	o.confirmation = confirmation.New(
		[]string{fmt.Sprintf("Delete workspace directory for %s?", next.name)},
		confirmation.WithStylePrefix("workspace delete"),
		confirmation.WithZIndex(render.ZDialogs),
		confirmation.WithOption("Yes", o.deleteDecisionCmd(true), key.NewBinding(key.WithKeys("y"), key.WithHelp("y", "yes"))),
		confirmation.WithOption("No", o.deleteDecisionCmd(false), key.NewBinding(key.WithKeys("n", "esc"), key.WithHelp("n/esc", "no"))),
	)
	o.confirmation.Styles.Border = common.DefaultPalette.GetBorder("workspace delete border", lipgloss.NormalBorder()).Padding(1)
	return o.confirmation.Init()
}

func (o *Operation) handleDeleteChoice(shouldDelete bool) tea.Cmd {
	if o.activeDelete == nil {
		return nil
	}
	deletePath := o.activeDelete.path
	deleteName := o.activeDelete.name
	o.activeDelete = nil
	o.confirmation = nil
	if shouldDelete {
		deleteCmd := o.deleteWorkspaceDir(deletePath, deleteName)
		nextCmd := o.showNextDeletePrompt()
		if nextCmd != nil {
			return tea.Sequence(deleteCmd, nextCmd)
		}
		return deleteCmd
	}
	return o.showNextDeletePrompt()
}

func (o *Operation) deleteWorkspaceDir(path string, name string) tea.Cmd {
	return func() tea.Msg {
		if strings.TrimSpace(path) == "" {
			return common.CommandCompletedMsg{Err: fmt.Errorf("workspace path missing for %s", name)}
		}
		err := os.RemoveAll(path)
		if err != nil {
			return common.CommandCompletedMsg{Err: err}
		}
		return common.CommandCompletedMsg{Err: nil}
	}
}

func (o *Operation) finalizeIfDone() tea.Cmd {
	if o.pendingForget != 0 || o.activeDelete != nil || len(o.pendingDeletes) != 0 {
		return nil
	}
	return tea.Batch(common.Refresh, common.Close)
}

func (o *Operation) workspaceDirForName(name string) (string, error) {
	if name == "" {
		return "", errors.New("workspace name is required")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	dirName := filepath.Base(o.context.Location)
	return filepath.Join(home, "code", "jj-workspaces", dirName, name), nil
}

func (o *Operation) snapshotWorkspace(path string) tea.Cmd {
	if strings.TrimSpace(path) == "" {
		return nil
	}
	command := fmt.Sprintf("cd %q && jj debug snapshot", path)
	return o.context.RunProgramCommand("sh", []string{"-c", command})
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

func (o *Operation) copyWorkspacePathCmd(path string) tea.Cmd {
	return func() tea.Msg {
		if strings.TrimSpace(path) == "" {
			return common.CommandCompletedMsg{Err: errors.New("workspace path missing")}
		}
		if err := clipboard.WriteAll(path); err != nil {
			return common.CommandCompletedMsg{Err: err}
		}
		return common.CommandCompletedMsg{Err: nil}
	}
}

func workspacePath(location string, name string) (string, error) {
	if name == "" {
		return "", errors.New("workspace name is required")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	dirName := filepath.Base(location)
	baseDir := filepath.Join(home, "code", "jj-workspaces", dirName)
	if err := os.MkdirAll(baseDir, 0o755); err != nil {
		return "", err
	}
	return filepath.Join(baseDir, name), nil
}

func (o *Operation) deleteDecisionCmd(shouldDelete bool) tea.Cmd {
	return func() tea.Msg {
		return deleteDecisionMsg{delete: shouldDelete}
	}
}

func NewOperation(context *context.MainContext, selectedRevisions jj.SelectedRevisions) *Operation {
	styles := styles{
		sourceMarker: common.DefaultPalette.Get("workspace source_marker"),
	}
	return &Operation{
		context:           context,
		selectedRevisions: selectedRevisions,
		keyMap:            config.Current.GetKeyMap(),
		styles:            styles,
		forgetKey:         key.NewBinding(key.WithKeys("f"), key.WithHelp("f", "forget")),
	}
}
