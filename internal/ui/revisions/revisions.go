package revisions

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"

	"github.com/atotto/clipboard"
	"github.com/idursun/jjui/internal/ui/intents"
	"github.com/idursun/jjui/internal/ui/layout"
	"github.com/idursun/jjui/internal/ui/operations/ace_jump"
	"github.com/idursun/jjui/internal/ui/operations/ai_implement"
	"github.com/idursun/jjui/internal/ui/operations/duplicate"
	"github.com/idursun/jjui/internal/ui/operations/revert"
	"github.com/idursun/jjui/internal/ui/operations/set_parents"
	"github.com/idursun/jjui/internal/ui/render"

	"github.com/idursun/jjui/internal/parser"
	"github.com/idursun/jjui/internal/ui/operations/describe"

	"github.com/charmbracelet/bubbles/help"
	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/idursun/jjui/internal/config"
	"github.com/idursun/jjui/internal/jj"
	"github.com/idursun/jjui/internal/ui/common"
	appContext "github.com/idursun/jjui/internal/ui/context"
	"github.com/idursun/jjui/internal/ui/graph"
	"github.com/idursun/jjui/internal/ui/operations"
	"github.com/idursun/jjui/internal/ui/operations/abandon"
	"github.com/idursun/jjui/internal/ui/operations/bookmark"
	"github.com/idursun/jjui/internal/ui/operations/details"
	"github.com/idursun/jjui/internal/ui/operations/evolog"
	"github.com/idursun/jjui/internal/ui/operations/integrate"
	"github.com/idursun/jjui/internal/ui/operations/rebase"
	"github.com/idursun/jjui/internal/ui/operations/squash"
	"github.com/idursun/jjui/internal/ui/operations/workspace"
)

var (
	_              common.Focusable      = (*Model)(nil)
	_              common.Editable       = (*Model)(nil)
	_              common.ImmediateModel = (*Model)(nil)
	writeClipboard                       = clipboard.WriteAll
)

const revsetAutoExpandStep = 50

type Model struct {
	rows                   []parser.Row
	tag                    atomic.Uint64
	revisionToSelect       string
	offScreenRows          []parser.Row
	streamer               *graph.GraphStreamer
	hasMore                bool
	op                     common.ImmediateModel
	cursor                 int
	context                *appContext.MainContext
	keymap                 config.KeyMappings[key.Binding]
	output                 string
	err                    error
	quickSearch            string
	previousOpLogId        string
	isLoading              bool
	displayContextRenderer *DisplayContextRenderer
	textStyle              lipgloss.Style
	dimmedStyle            lipgloss.Style
	selectedStyle          lipgloss.Style
	matchedStyle           lipgloss.Style
	ensureCursorView       bool
	requestInFlight        bool
}

type revisionsMsg struct {
	msg tea.Msg
}

func RevisionsCmd(msg tea.Msg) tea.Cmd {
	return func() tea.Msg {
		return revisionsMsg{msg: msg}
	}
}

type ItemClickedMsg struct {
	Index int
}

type ViewportScrollMsg struct {
	Delta      int
	Horizontal bool
}

func (v ViewportScrollMsg) SetDelta(delta int, horizontal bool) tea.Msg {
	v.Delta = delta
	v.Horizontal = horizontal
	return v
}

type updateRevisionsMsg struct {
	rows             []parser.Row
	selectedRevision string
}

type startRowsStreamingMsg struct {
	selectedRevision string
	tag              uint64
}

type appendRowsBatchMsg struct {
	rows    []parser.Row
	hasMore bool
	tag     uint64
}

func (m *Model) Cursor() int {
	return m.cursor
}

func (m *Model) SetCursor(index int) {
	if index >= 0 && index < len(m.rows) {
		m.cursor = index
		m.ensureCursorView = true
	}
}

func (m *Model) HasMore() bool {
	return m.hasMore
}

func (m *Model) Scroll(delta int) tea.Cmd {
	m.ensureCursorView = false
	currentStart := m.displayContextRenderer.GetScrollOffset()
	desiredStart := currentStart + delta
	m.displayContextRenderer.SetScrollOffset(desiredStart)

	// Request more rows if scrolling down and near the end
	if m.hasMore && delta > 0 {
		lastRowIndex := m.displayContextRenderer.GetLastRowIndex()
		if lastRowIndex >= len(m.rows)-1 {
			return m.requestMoreRows(m.tag.Load())
		}
	}

	if delta > 0 {
		lastRowIndex := m.displayContextRenderer.GetLastRowIndex()
		if lastRowIndex >= len(m.rows)-1 {
			return m.maybeExpandRevsetFromBottom()
		}
	}
	return nil
}

func (m *Model) Len() int {
	return len(m.rows)
}

func (m *Model) IsEditing() bool {
	if f, ok := m.op.(common.Editable); ok {
		return f.IsEditing()
	}
	return false
}

func (m *Model) IsFocused() bool {
	if f, ok := m.op.(common.Focusable); ok {
		return f.IsFocused()
	}
	return false
}

func (m *Model) InNormalMode() bool {
	if _, ok := m.op.(*operations.Default); ok {
		return true
	}
	return false
}

func (m *Model) OperationName() string {
	if op, ok := m.op.(operations.Operation); ok {
		return op.Name()
	}
	return ""
}

func (m *Model) ShortHelp() []key.Binding {
	if op, ok := m.op.(help.KeyMap); ok {
		return op.ShortHelp()
	}
	return (&operations.Default{}).ShortHelp()
}

func (m *Model) FullHelp() [][]key.Binding {
	if op, ok := m.op.(help.KeyMap); ok {
		return op.FullHelp()
	}
	return [][]key.Binding{m.ShortHelp()}
}

func (m *Model) SelectedRevision() *jj.Commit {
	if m.cursor >= len(m.rows) || m.cursor < 0 {
		return nil
	}
	return m.rows[m.cursor].Commit
}

func (m *Model) SelectedRevisions() jj.SelectedRevisions {
	var selected []*jj.Commit
	ids := make(map[string]bool)
	for _, ci := range m.context.CheckedItems {
		if rev, ok := ci.(appContext.SelectedRevision); ok {
			ids[rev.CommitId] = true
		}
	}
	for _, row := range m.rows {
		if _, ok := ids[row.Commit.CommitId]; ok {
			selected = append(selected, row.Commit)
		}
	}

	if len(selected) == 0 {
		return jj.NewSelectedRevisions(m.SelectedRevision())
	}
	return jj.NewSelectedRevisions(selected...)
}

func (m *Model) Init() tea.Cmd {
	return common.RefreshAndSelect("@")
}

func (m *Model) Update(msg tea.Msg) tea.Cmd {
	if k, ok := msg.(revisionsMsg); ok {
		msg = k.msg
	}
	cmd := m.internalUpdate(msg)

	if curSelected := m.SelectedRevision(); curSelected != nil {
		if op, ok := m.op.(operations.TracksSelectedRevision); ok {
			cmd = tea.Batch(cmd, op.SetSelectedRevision(curSelected))
		}
	}

	return cmd
}

func (m *Model) internalUpdate(msg tea.Msg) tea.Cmd {
	switch msg := msg.(type) {
	case intents.Intent:
		return m.handleIntent(msg)
	case ItemClickedMsg:
		// Don't allow changing selection if the operation is editing (e.g. describe)
		if editable, ok := m.op.(common.Editable); ok && editable.IsEditing() {
			return nil
		}
		// Don't allow changing selection if the operation is an overlay (e.g. details)
		if overlay, ok := m.op.(common.Overlay); ok && overlay.IsOverlay() {
			return nil
		}
		m.SetCursor(msg.Index)
		return m.updateSelection()
	case ViewportScrollMsg:
		if msg.Horizontal {
			return nil
		}
		return m.Scroll(msg.Delta)

	case common.CloseViewMsg:
		m.op = operations.NewDefault()
		return m.updateSelection()
	case common.RestoreOperationMsg:
		if op, ok := msg.Operation.(operations.Operation); ok {
			m.op = op
			return m.updateSelection()
		}
		m.op = operations.NewDefault()
		return m.updateSelection()
	case common.QuickSearchMsg:
		m.quickSearch = strings.ToLower(string(msg))
		m.SetCursor(m.search(0, false))
		m.op = operations.NewDefault()
		return m.updateSelection()
	case common.CommandCompletedMsg:
		m.output = msg.Output
		m.err = msg.Err
		return nil
	case common.AutoRefreshMsg:
		id, _ := m.context.RunCommandImmediate(jj.OpLogId(true))
		currentOperationId := string(id)
		log.Println("Previous operation ID:", m.previousOpLogId, "Current operation ID:", currentOperationId)
		if currentOperationId != m.previousOpLogId {
			m.previousOpLogId = currentOperationId
			return common.RefreshAndKeepSelections
		}
	case common.UpdateRevisionsFailedMsg:
		m.isLoading = false
		return nil
	case common.RefreshMsg:
		return tea.Batch(m.refresh(intents.Refresh{
			KeepSelections:   msg.KeepSelections,
			SelectedRevision: msg.SelectedRevision,
		}), m.op.Update(msg))
	case updateRevisionsMsg:
		m.isLoading = false
		m.updateGraphRows(msg.rows, msg.selectedRevision)
		return tea.Batch(m.highlightChanges, m.updateSelection(), func() tea.Msg {
			return common.UpdateRevisionsSuccessMsg{}
		})
	case startRowsStreamingMsg:
		m.offScreenRows = nil
		m.revisionToSelect = msg.selectedRevision

		// If the revision to select is not set, use the currently selected item
		if m.revisionToSelect == "" {
			switch selected := m.context.SelectedItem.(type) {
			case appContext.SelectedRevision:
				m.revisionToSelect = selected.CommitId
			case appContext.SelectedFile:
				m.revisionToSelect = selected.CommitId
			}
		}
		log.Println("Starting streaming revisions with tag:", msg.tag)
		return m.requestMoreRows(msg.tag)
	case appendRowsBatchMsg:
		m.requestInFlight = false

		if msg.tag != m.tag.Load() {
			return nil
		}
		m.offScreenRows = append(m.offScreenRows, msg.rows...)
		m.hasMore = msg.hasMore
		m.isLoading = m.hasMore && len(m.offScreenRows) > 0

		if m.hasMore {
			// keep requesting rows until we reach the initial load count or the current cursor position
			lastRowIndex := m.displayContextRenderer.GetLastRowIndex()
			if len(m.offScreenRows) < m.cursor+1 || len(m.offScreenRows) < lastRowIndex+1 {
				return m.requestMoreRows(msg.tag)
			}
		} else if m.streamer != nil {
			m.streamer.Close()
		}

		currentSelectedRevision := m.SelectedRevision()
		m.rows = m.offScreenRows
		if m.revisionToSelect != "" {
			m.SetCursor(m.selectRevision(m.revisionToSelect))
			m.revisionToSelect = ""
		}

		if m.cursor == -1 && currentSelectedRevision != nil {
			m.SetCursor(m.selectRevision(currentSelectedRevision.GetChangeId()))
		}

		if (m.cursor < 0 || m.cursor >= len(m.rows)) && len(m.rows) > 0 {
			m.SetCursor(0)
		}

		cmds := []tea.Cmd{m.highlightChanges, m.updateSelection()}
		if len(m.offScreenRows) > 0 {
			cmds = append(cmds, func() tea.Msg {
				return common.UpdateRevisionsSuccessMsg{}
			})
		}
		return tea.Batch(cmds...)
	}

	if intent, ok := msg.(intents.Intent); ok {
		if cmd := m.handleIntent(intent); cmd != nil {
			return cmd
		}
		return m.op.Update(msg)
	}

	// Non-input messages are broadcast to the current operation
	if !common.IsInputMessage(msg) {
		return m.op.Update(msg)
	}

	if len(m.rows) == 0 {
		return nil
	}

	if op, ok := m.op.(common.Editable); ok && op.IsEditing() {
		return m.op.Update(msg)
	}

	if op, ok := m.op.(common.Overlay); ok && op.IsOverlay() {
		return m.op.Update(msg)
	}

	var cmd tea.Cmd
	switch msg := msg.(type) {
	case tea.KeyMsg:
		if op, ok := m.op.(*details.Operation); ok && op.IsFocused() {
			return op.Update(msg)
		}
		switch {
		case key.Matches(msg, m.keymap.Up, m.keymap.ScrollUp):
			return m.handleIntent(intents.Navigate{Delta: -1, IsPage: key.Matches(msg, m.keymap.ScrollUp)})
		case key.Matches(msg, m.keymap.Down, m.keymap.ScrollDown):
			return m.handleIntent(intents.Navigate{Delta: 1, IsPage: key.Matches(msg, m.keymap.ScrollDown)})
		case key.Matches(msg, m.keymap.JumpToParent):
			return m.handleIntent(intents.Navigate{Target: intents.TargetParent})
		case key.Matches(msg, m.keymap.JumpToChildren):
			return m.handleIntent(intents.Navigate{Target: intents.TargetChild})
		case key.Matches(msg, m.keymap.JumpToWorkingCopy):
			return m.handleIntent(intents.Navigate{Target: intents.TargetWorkingCopy})
		default:
			if op, ok := m.op.(common.Focusable); ok && op.IsFocused() {
				return m.op.Update(msg)
			}

			switch {
			case m.quickSearch != "" && (msg.Type == tea.KeyEsc || msg.Type == tea.KeyEnter):
				return m.handleIntent(intents.RevisionsQuickSearchClear{})
			case key.Matches(msg, m.keymap.AceJump):
				return m.handleIntent(intents.StartAceJump{})
			case key.Matches(msg, m.keymap.ToggleSelect):
				return m.handleIntent(intents.RevisionsToggleSelect{})
			case key.Matches(msg, m.keymap.Cancel):
				return m.handleIntent(intents.Cancel{})
			case m.quickSearch != "" &&
				(key.Matches(msg, m.keymap.QuickSearchNext) ||
					key.Matches(msg, m.keymap.QuickSearchPrev)):
				reverse := key.Matches(msg, m.keymap.QuickSearchPrev)
				return m.handleIntent(intents.QuickSearchCycle{Reverse: reverse})
			case key.Matches(msg, m.keymap.CopyCommitSHA):
				return m.handleIntent(intents.CopyCommitSHA{})
			case key.Matches(msg, m.keymap.Details.Mode):
				return m.handleIntent(intents.OpenDetails{})
			case key.Matches(msg, m.keymap.InlineDescribe.Mode):
				return m.handleIntent(intents.StartInlineDescribe{})
			case key.Matches(msg, m.keymap.New):
				return m.handleIntent(intents.StartNew{})
			case key.Matches(msg, m.keymap.NewNoEdit):
				return m.handleIntent(intents.StartNewNoEdit{})
			case key.Matches(msg, m.keymap.Commit):
				return m.handleIntent(intents.CommitWorkingCopy{})
			case key.Matches(msg, m.keymap.Edit, m.keymap.ForceEdit):
				ignoreImmutable := key.Matches(msg, m.keymap.ForceEdit)
				return m.handleIntent(intents.StartEdit{IgnoreImmutable: ignoreImmutable})
			case key.Matches(msg, m.keymap.Diffedit):
				return m.handleIntent(intents.StartDiffEdit{})
			case key.Matches(msg, m.keymap.Absorb):
				return m.handleIntent(intents.StartAbsorb{})
			case key.Matches(msg, m.keymap.Abandon):
				return m.handleIntent(intents.StartAbandon{})
			case key.Matches(msg, m.keymap.AiImplement):
				return m.handleIntent(intents.StartAiImplement{})
			case key.Matches(msg, m.keymap.Workspace):
				return m.handleIntent(intents.StartWorkspace{})
			case key.Matches(msg, m.keymap.SelectAiAncestors):
				return m.handleIntent(intents.SelectAiAncestors{})
			case key.Matches(msg, m.keymap.Integrate):
				return m.handleIntent(intents.StartIntegrate{})
			case key.Matches(msg, m.keymap.Restack):
				return m.handleIntent(intents.StartRestack{})
			case key.Matches(msg, m.keymap.Bookmark.Set):
				return m.handleIntent(intents.BookmarksSet{})
			case key.Matches(msg, m.keymap.Split, m.keymap.SplitParallel):
				return m.handleIntent(intents.StartSplit{
					IsParallel: key.Matches(msg, m.keymap.SplitParallel),
				})
			case key.Matches(msg, m.keymap.Describe):
				return m.handleIntent(intents.StartDescribe{})
			case key.Matches(msg, m.keymap.Evolog.Mode):
				return m.handleIntent(intents.StartEvolog{})
			case key.Matches(msg, m.keymap.Diff):
				return m.handleIntent(intents.ShowDiff{})
			case key.Matches(msg, m.keymap.Refresh):
				return m.handleIntent(intents.Refresh{})
			case key.Matches(msg, m.keymap.Squash.Mode):
				return m.handleIntent(intents.StartSquash{})
			case key.Matches(msg, m.keymap.Revert.Mode):
				return m.handleIntent(intents.StartRevert{})
			case key.Matches(msg, m.keymap.Rebase.Mode):
				return m.handleIntent(intents.StartRebase{})
			case key.Matches(msg, m.keymap.Duplicate.Mode):
				return m.handleIntent(intents.StartDuplicate{})
			case key.Matches(msg, m.keymap.SetParents):
				return m.handleIntent(intents.SetParents{})
			}
		}
	}

	return cmd
}

func (m *Model) handleIntent(intent intents.Intent) tea.Cmd {
	switch intent := intent.(type) {
	case intents.OpenDetails:
		return m.openDetails(intent)
	case intents.StartSquash:
		return m.startSquash(intent)
	case intents.StartInlineDescribe:
		return m.startInlineDescribe(intent)
	case intents.StartAbsorb:
		return m.startAbsorb(intent)
	case intents.StartAbandon:
		return m.startAbandon(intent)
	case intents.StartAiImplement:
		return m.startAiImplement(intent)
	case intents.StartWorkspace:
		return m.startWorkspace(intent)
	case intents.StartIntegrate:
		return m.startIntegrate(intent)
	case intents.StartRestack:
		return m.startRestack()
	case intents.SelectAiAncestors:
		return m.selectAiAncestors()
	case intents.StartNew:
		return m.startNew(intent)
	case intents.StartNewNoEdit:
		return m.startNewNoEdit(intent)
	case intents.CommitWorkingCopy:
		return m.commitWorkingCopy()
	case intents.StartEdit:
		return m.startEdit(intent)
	case intents.StartDiffEdit:
		return m.startDiffEdit(intent)
	case intents.StartRevert:
		return m.startRevert(intent)
	case intents.StartDuplicate:
		return m.startDuplicate(intent)
	case intents.SetParents:
		return m.startSetParents(intent)
	case intents.BookmarksSet:
		return m.startBookmarkSet()
	case intents.RevisionsToggleSelect:
		commit := m.rows[m.cursor].Commit
		changeId := commit.GetChangeId()
		item := appContext.SelectedRevision{ChangeId: changeId, CommitId: commit.CommitId}
		m.context.ToggleCheckedItem(item)
		return m.navigate(intents.Navigate{Delta: 1})
	case intents.Navigate:
		return m.navigate(intent)
	case intents.StartDescribe:
		return m.startDescribe(intent)
	case intents.StartEvolog:
		return m.startEvolog(intent)
	case intents.ShowDiff:
		return m.showDiff(intent)
	case intents.StartSplit:
		return m.startSplit(intent)
	case intents.StartRebase:
		return m.startRebase(intent)
	case intents.Refresh:
		return m.refresh(intent)
	case intents.Cancel:
		m.context.ClearCheckedItems(reflect.TypeFor[appContext.SelectedRevision]())
		m.op = operations.NewDefault()
		return nil
	case intents.QuickSearchCycle:
		offset := 1
		if intent.Reverse {
			offset = -1
		}
		m.SetCursor(m.search(m.cursor+offset, intent.Reverse))
		return m.updateSelection()
	case intents.RevisionsQuickSearchClear:
		m.quickSearch = ""
		return nil
	case intents.CopyCommitSHA:
		return m.copySelectedCommitSHA()
	case intents.StartAceJump:
		parentOp := m.op
		// Create ace jump with parent operation
		op := ace_jump.NewOperation(m.SetCursor, func(index int) parser.Row {
			return m.rows[index]
		}, m.displayContextRenderer.GetFirstRowIndex(), m.displayContextRenderer.GetLastRowIndex(), parentOp)
		m.op = op
		return op.Init()
	}
	return nil
}

func (m *Model) copySelectedCommitSHA() tea.Cmd {
	revision := m.SelectedRevision()
	if revision == nil || strings.TrimSpace(revision.CommitId) == "" {
		return func() tea.Msg {
			return intents.AddMessage{Err: errors.New("no revision selected")}
		}
	}

	fullSHA, err := m.context.RunCommandImmediate(jj.GetFullCommitIDFromRevision(revision.CommitId))
	if err != nil {
		return func() tea.Msg {
			return intents.AddMessage{Err: err}
		}
	}

	sha := strings.TrimSpace(string(fullSHA))
	if sha == "" {
		return func() tea.Msg {
			return intents.AddMessage{Err: errors.New("could not resolve commit sha")}
		}
	}
	if len(sha) > 8 {
		sha = sha[:8]
	}
	return func() tea.Msg {
		if err := writeClipboard(sha); err != nil {
			return intents.AddMessage{Err: err}
		}
		return intents.AddMessage{Text: "Copied commit SHA: " + sha}
	}
}

func (m *Model) startBookmarkSet() tea.Cmd {
	rev := m.SelectedRevision()
	if rev == nil {
		return nil
	}
	m.op = bookmark.NewSetBookmarkOperation(m.context, rev.GetChangeId())
	return m.op.Init()
}

func (m *Model) refresh(intent intents.Refresh) tea.Cmd {
	if !intent.KeepSelections {
		m.context.ClearCheckedItems(reflect.TypeFor[appContext.SelectedRevision]())
	}
	m.isLoading = true
	if config.Current.Revisions.LogBatching {
		currentTag := m.tag.Add(1)
		return m.loadStreaming(m.context.CurrentRevset, intent.SelectedRevision, currentTag)
	}
	return m.load(m.context.CurrentRevset, intent.SelectedRevision)
}

func (m *Model) openDetails(_ intents.OpenDetails) tea.Cmd {
	if m.SelectedRevision() == nil {
		return nil
	}
	model := details.NewOperation(m.context, m.SelectedRevision())
	m.op = model
	return m.op.Init()
}

func (m *Model) startSquash(intent intents.StartSquash) tea.Cmd {
	selected := intent.Selected
	if len(selected.Revisions) == 0 {
		selected = m.SelectedRevisions()
	}
	if len(selected.Revisions) == 0 {
		return nil
	}

	parent, _ := m.context.RunCommandImmediate(jj.GetParent(selected))
	parentIdx := m.selectRevision(string(parent))
	if parentIdx != -1 {
		m.SetCursor(parentIdx)
	} else if m.cursor < len(m.rows)-1 {
		m.SetCursor(m.cursor + 1)
	}
	m.op = squash.NewOperation(m.context, selected, squash.WithFiles(intent.Files))
	return tea.Batch(m.op.Init(), m.updateSelection())
}

func (m *Model) startRebase(intent intents.StartRebase) tea.Cmd {
	selected := intent.Selected
	if len(selected.Revisions) == 0 {
		selected = m.SelectedRevisions()
	}
	if len(selected.Revisions) == 0 {
		return nil
	}

	source := rebaseSourceFromIntent(intent.Source)
	target := rebaseTargetFromIntent(intent.Target)
	m.op = rebase.NewOperation(m.context, selected, source, target)
	return m.op.Init()
}

func (m *Model) startRevert(intent intents.StartRevert) tea.Cmd {
	selected := intent.Selected
	if len(selected.Revisions) == 0 {
		selected = m.SelectedRevisions()
	}
	if len(selected.Revisions) == 0 {
		return nil
	}

	target := revertTargetFromIntent(intent.Target)
	m.op = revert.NewOperation(m.context, selected, target)
	return m.op.Init()
}

func rebaseSourceFromIntent(source intents.RebaseSource) rebase.Source {
	switch source {
	case intents.RebaseSourceBranch:
		return rebase.SourceBranch
	case intents.RebaseSourceDescendants:
		return rebase.SourceDescendants
	default:
		return rebase.SourceRevision
	}
}

func rebaseTargetFromIntent(target intents.RebaseTarget) rebase.Target {
	switch target {
	case intents.RebaseTargetAfter:
		return rebase.TargetAfter
	case intents.RebaseTargetBefore:
		return rebase.TargetBefore
	case intents.RebaseTargetInsert:
		return rebase.TargetInsert
	default:
		return rebase.TargetDestination
	}
}

func revertTargetFromIntent(target intents.RevertTarget) revert.Target {
	switch target {
	case intents.RevertTargetAfter:
		return revert.TargetAfter
	case intents.RevertTargetBefore:
		return revert.TargetBefore
	case intents.RevertTargetInsert:
		return revert.TargetInsert
	default:
		return revert.TargetDestination
	}
}

func (m *Model) startDuplicate(intent intents.StartDuplicate) tea.Cmd {
	selected := intent.Selected
	if len(selected.Revisions) == 0 {
		selected = m.SelectedRevisions()
	}
	if len(selected.Revisions) == 0 {
		return nil
	}

	m.op = duplicate.NewOperation(m.context, selected, duplicate.TargetDestination)
	return m.op.Init()
}

func (m *Model) startSetParents(intent intents.SetParents) tea.Cmd {
	commit := intent.Selected
	if commit == nil {
		commit = m.SelectedRevision()
	}
	if commit == nil {
		return nil
	}

	m.op = set_parents.NewModel(m.context, commit)
	return m.op.Init()
}

func (m *Model) startNew(intent intents.StartNew) tea.Cmd {
	selected := intent.Selected
	if len(selected.Revisions) == 0 {
		selected = m.SelectedRevisions()
	}
	return m.context.RunCommand(jj.New(selected, false), common.RefreshAndSelect("@"))
}

func (m *Model) startNewNoEdit(intent intents.StartNewNoEdit) tea.Cmd {
	selected := intent.Selected
	if len(selected.Revisions) == 0 {
		selected = m.SelectedRevisions()
	}
	return m.context.RunCommand(jj.New(selected, true), func() tea.Msg {
		base := selected.Last()
		if base == "" {
			base = "@"
		}
		revset := fmt.Sprintf("latest(children(%s), 1)", base)
		output, err := m.context.RunCommandImmediate(jj.GetFullIdsFromRevset(revset))
		if err != nil {
			return common.CommandCompletedMsg{Output: string(output), Err: err}
		}
		changeID := strings.TrimSpace(string(output))
		if changeID == "" {
			return common.RefreshAndSelect("@")()
		}
		return common.RefreshAndSelect(changeID)()
	})
}

func (m *Model) commitWorkingCopy() tea.Cmd {
	return m.context.RunInteractiveCommand(jj.CommitWorkingCopy(), common.Refresh)
}

func (m *Model) startEdit(intent intents.StartEdit) tea.Cmd {
	commit := intent.Selected
	if commit == nil {
		commit = m.SelectedRevision()
	}
	if commit == nil {
		return nil
	}
	return m.context.RunCommand(jj.Edit(commit.GetChangeId(), intent.IgnoreImmutable), common.Refresh)
}

func (m *Model) startDiffEdit(intent intents.StartDiffEdit) tea.Cmd {
	commit := intent.Selected
	if commit == nil {
		commit = m.SelectedRevision()
	}
	if commit == nil {
		return nil
	}
	return m.context.RunInteractiveCommand(jj.DiffEdit(commit.GetChangeId()), common.Refresh)
}

func (m *Model) startAbsorb(intent intents.StartAbsorb) tea.Cmd {
	commit := intent.Selected
	if commit == nil {
		commit = m.SelectedRevision()
	}
	if commit == nil {
		return nil
	}
	return m.context.RunCommand(jj.Absorb(commit.GetChangeId()), common.Refresh)
}

func (m *Model) startAbandon(intent intents.StartAbandon) tea.Cmd {
	selected := intent.Selected
	if len(selected.Revisions) == 0 {
		selected = m.SelectedRevisions()
	}
	if len(selected.Revisions) == 0 {
		return nil
	}
	m.op = abandon.NewOperation(m.context, selected)
	return m.op.Init()
}

func (m *Model) startAiImplement(intent intents.StartAiImplement) tea.Cmd {
	selected := intent.Selected
	if len(selected.Revisions) == 0 {
		selected = m.SelectedRevisions()
	}
	if len(selected.Revisions) == 0 {
		return nil
	}
	m.op = ai_implement.NewOperation(m.context, selected)
	return m.op.Init()
}

func (m *Model) startIntegrate(intent intents.StartIntegrate) tea.Cmd {
	selected := intent.Selected
	if len(selected.Revisions) == 0 {
		selected = m.SelectedRevisions()
	}
	if len(selected.Revisions) == 0 {
		return nil
	}
	m.op = integrate.NewOperation(m.context, selected)
	return m.op.Init()
}

func (m *Model) startWorkspace(intent intents.StartWorkspace) tea.Cmd {
	selected := intent.Selected
	if len(selected.Revisions) == 0 {
		selected = m.SelectedRevisions()
	}
	if len(selected.Revisions) == 0 {
		return nil
	}
	m.op = workspace.NewOperation(m.context, selected)
	return m.op.Init()
}

func (m *Model) startRestack() tea.Cmd {
	return m.context.RunCommand(jj.Restack(), common.Refresh)
}

func (m *Model) selectAiAncestors() tea.Cmd {
	output, err := m.context.RunCommandImmediate(jj.GetIdsFromRevset("::@ & description(glob:\"ai:*\")"))
	if err != nil {
		return func() tea.Msg {
			return common.CommandCompletedMsg{Err: err}
		}
	}
	lines := strings.Split(strings.TrimSpace(string(output)), "\n")
	selectedIds := make(map[string]struct{}, len(lines))
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		selectedIds[line] = struct{}{}
	}
	if len(selectedIds) == 0 {
		return nil
	}
	for _, row := range m.rows {
		commit := row.Commit
		if commit == nil {
			continue
		}
		if _, ok := selectedIds[commit.GetChangeId()]; !ok {
			continue
		}
		item := appContext.SelectedRevision{ChangeId: commit.GetChangeId(), CommitId: commit.CommitId}
		m.context.AddCheckedItem(item)
	}
	return m.updateSelection()
}

func (m *Model) navigate(intent intents.Navigate) tea.Cmd {
	if len(m.rows) == 0 {
		return nil
	}

	ensureView := true
	if intent.EnsureView != nil {
		ensureView = *intent.EnsureView
	}
	allowStream := true
	if intent.AllowStream != nil {
		allowStream = *intent.AllowStream
	}

	if intent.ChangeID != "" || intent.FallbackID != "" {
		idx := m.selectRevision(intent.ChangeID)
		if idx == -1 && intent.FallbackID != "" {
			idx = m.selectRevision(intent.FallbackID)
		}
		if idx == -1 {
			return nil
		}
		m.ensureCursorView = ensureView
		m.SetCursor(idx)
		return m.updateSelection()
	}

	switch intent.Target {
	case intents.TargetParent:
		m.jumpToParent(m.SelectedRevisions())
		m.ensureCursorView = ensureView
		return m.updateSelection()
	case intents.TargetWorkingCopy:
		if idx := m.selectRevision("@"); idx != -1 {
			m.SetCursor(idx)
		}
		m.ensureCursorView = ensureView
		return m.updateSelection()
	case intents.TargetChild:
		immediate, _ := m.context.RunCommandImmediate(jj.GetFirstChild(m.SelectedRevision()))
		if idx := m.selectRevision(string(immediate)); idx != -1 {
			m.SetCursor(idx)
		}
		m.ensureCursorView = ensureView
		return m.updateSelection()
	}

	delta := intent.Delta
	if delta == 0 {
		delta = 1
	}

	// Calculate step (convert page scroll to item count)
	step := delta
	if intent.IsPage {
		firstRowIndex := m.displayContextRenderer.GetFirstRowIndex()
		lastRowIndex := m.displayContextRenderer.GetLastRowIndex()
		span := max(lastRowIndex-firstRowIndex-1, 1)
		if step < 0 {
			step = -span
		} else {
			step = span
		}
	}

	// Calculate new cursor position
	totalItems := len(m.rows)
	newCursor := m.cursor + step

	if step > 0 {
		// Moving down
		if newCursor >= totalItems {
			if allowStream && m.hasMore {
				return m.requestMoreRows(m.tag.Load())
			}
			if allowStream {
				if cmd := m.maybeExpandRevsetFromBottom(); cmd != nil {
					return cmd
				}
			}
			newCursor = totalItems - 1
		}
	} else {
		// Moving up
		if newCursor < 0 {
			newCursor = 0
		}
	}

	m.SetCursor(newCursor)
	m.ensureCursorView = ensureView
	return m.updateSelection()
}

func (m *Model) startDescribe(intent intents.StartDescribe) tea.Cmd {
	selected := intent.Selected
	if len(selected.Revisions) == 0 {
		selected = m.SelectedRevisions()
	}
	if len(selected.Revisions) == 0 {
		return nil
	}
	return m.context.RunInteractiveCommand(jj.Describe(selected), common.Refresh)
}

func (m *Model) startEvolog(intent intents.StartEvolog) tea.Cmd {
	commit := intent.Selected
	if commit == nil {
		commit = m.SelectedRevision()
	}
	if commit == nil {
		return nil
	}
	model := evolog.NewOperation(m.context, commit)
	m.op = model
	return m.op.Init()
}

func (m *Model) startInlineDescribe(intent intents.StartInlineDescribe) tea.Cmd {
	commit := intent.Selected
	if commit == nil {
		commit = m.SelectedRevision()
	}
	if commit == nil {
		return nil
	}
	model := describe.NewOperation(m.context, commit)
	m.op = model
	return m.op.Init()
}

func (m *Model) showDiff(intent intents.ShowDiff) tea.Cmd {
	commit := intent.Selected
	if commit == nil {
		commit = m.SelectedRevision()
	}
	if commit == nil {
		return nil
	}
	changeId := commit.GetChangeId()
	return func() tea.Msg {
		output, _ := m.context.RunCommandImmediate(jj.Diff(changeId, ""))
		return common.ShowDiffMsg(output)
	}
}

func (m *Model) startSplit(intent intents.StartSplit) tea.Cmd {
	commit := intent.Selected
	if commit == nil {
		commit = m.SelectedRevision()
	}
	if commit == nil {
		return nil
	}
	return m.context.RunInteractiveCommand(jj.Split(commit.GetChangeId(), intent.Files, intent.IsParallel, intent.IsInteractive), common.Refresh)
}

func (m *Model) updateSelection() tea.Cmd {
	// Don't override file-level selections (from Details panel)
	if _, isFile := m.context.SelectedItem.(appContext.SelectedFile); isFile && !m.InNormalMode() {
		return nil
	}
	if selectedRevision := m.SelectedRevision(); selectedRevision != nil {
		return m.context.SetSelectedItem(appContext.SelectedRevision{
			ChangeId: selectedRevision.GetChangeId(),
			CommitId: selectedRevision.CommitId,
		})
	}
	return nil
}

func (m *Model) highlightChanges() tea.Msg {
	if m.err != nil || m.output == "" {
		return nil
	}

	changes := strings.Split(m.output, "\n")
	for _, change := range changes {
		if !strings.HasPrefix(change, " ") {
			continue
		}
		line := strings.Trim(change, "\n ")
		if line == "" {
			continue
		}
		parts := strings.Split(line, " ")
		if len(parts) > 0 {
			for i := range m.rows {
				row := &m.rows[i]
				if strings.HasPrefix(parts[0], row.Commit.GetChangeId()) {
					row.IsAffected = true
					break
				}
			}
		}
	}
	return nil
}

func (m *Model) updateGraphRows(rows []parser.Row, selectedRevision string) {
	if rows == nil {
		rows = []parser.Row{}
	}

	currentSelectedRevision := selectedRevision
	if cur := m.SelectedRevision(); currentSelectedRevision == "" && cur != nil {
		currentSelectedRevision = cur.GetChangeId()
	}
	m.rows = rows

	if len(m.rows) > 0 {
		m.SetCursor(m.selectRevision(currentSelectedRevision))
		if m.cursor == -1 {
			m.SetCursor(m.selectRevision("@"))
		}
		if m.cursor == -1 {
			m.SetCursor(0)
		}
	} else {
		m.SetCursor(0)
	}
}

func (m *Model) ViewRect(dl *render.DisplayContext, box layout.Box) {
	if len(m.rows) == 0 {
		content := ""
		if m.isLoading {
			content = lipgloss.Place(box.R.Dx(), box.R.Dy(), lipgloss.Center, lipgloss.Center, "loading")
		} else {
			content = lipgloss.Place(box.R.Dx(), box.R.Dy(), lipgloss.Center, lipgloss.Center, "(no matching revisions)")
		}
		dl.AddDraw(box.R, content, 0)
		return
	}

	// Set selections
	m.displayContextRenderer.SetSelections(m.context.GetSelectedRevisions())

	// Get operation if any
	var op operations.Operation
	if opModel, ok := m.op.(operations.Operation); ok {
		op = opModel
	}

	// Render to DisplayContext
	m.displayContextRenderer.Render(
		dl,
		m.rows,
		m.cursor,
		box,
		op,
		m.quickSearch,
		m.ensureCursorView,
	)

	if overlayOp, ok := m.op.(common.Overlay); ok && overlayOp.IsOverlay() {
		m.op.ViewRect(dl, box)
	}

	// Reset the flag after ensuring cursor is visible
	m.ensureCursorView = false
}

func (m *Model) load(revset string, selectedRevision string) tea.Cmd {
	return func() tea.Msg {
		output, err := m.context.RunCommandImmediate(jj.Log(revset, config.Current.Limit, m.context.JJConfig.Templates.Log))
		if err != nil {
			return common.UpdateRevisionsFailedMsg{
				Err:    err,
				Output: string(output),
			}
		}
		rows := parser.ParseRows(bytes.NewReader(output))
		return updateRevisionsMsg{rows, selectedRevision}
	}
}

func (m *Model) loadStreaming(revset string, selectedRevision string, tag uint64) tea.Cmd {
	if m.tag.Load() != tag {
		return nil
	}

	var cmds []tea.Cmd
	streamer, err := graph.NewGraphStreamer(context.Background(), m.context, revset, m.context.JJConfig.Templates.Log)
	if err != nil {
		var errMsg string
		if err == io.EOF {
			errMsg = fmt.Sprintf("No revisions found for revset `%s`", revset)
			err = errors.New(errMsg)
		} else {
			errMsg = fmt.Sprintf("%v", err)
		}

		cmds = append(cmds, func() tea.Msg {
			return common.UpdateRevisionsFailedMsg{
				Err:    err,
				Output: errMsg,
			}
		})
	}

	if m.streamer != nil {
		m.streamer.Close()
		m.streamer = nil
	}
	m.streamer = streamer
	m.hasMore = true
	cmds = append(cmds, m.Update(startRowsStreamingMsg{selectedRevision: selectedRevision, tag: tag}))
	return tea.Batch(cmds...)
}

func (m *Model) requestMoreRows(tag uint64) tea.Cmd {
	if m.requestInFlight || m.streamer == nil || !m.hasMore || tag != m.tag.Load() {
		return nil
	}

	m.requestInFlight = true
	batch := m.streamer.RequestMore()
	return m.Update(appendRowsBatchMsg{batch.Rows, batch.HasMore, tag})
}

func (m *Model) maybeExpandRevsetFromBottom() tea.Cmd {
	if m.isLoading || len(m.rows) == 0 {
		return nil
	}

	nextRevset := expandRevsetForMoreHistory(m.context.CurrentRevset, m.oldestVisibleChangeID(), revsetAutoExpandStep)
	if nextRevset == "" || nextRevset == m.context.CurrentRevset {
		return nil
	}

	return common.UpdateRevSet(nextRevset)
}

func (m *Model) oldestVisibleChangeID() string {
	for i := len(m.rows) - 1; i >= 0; i-- {
		if m.rows[i].Commit == nil {
			continue
		}

		changeID := strings.TrimSpace(m.rows[i].Commit.GetChangeId())
		if changeID == "" {
			continue
		}

		return changeID
	}

	return ""
}

func expandRevsetForMoreHistory(revset string, fallbackAncestorBase string, step int) string {
	revset = strings.TrimSpace(revset)
	if revset == "" || step <= 0 {
		return ""
	}

	if expanded, ok := incrementLargestAncestorsRange(revset, step); ok {
		return expanded
	}

	fallbackAncestorBase = strings.TrimSpace(fallbackAncestorBase)
	if fallbackAncestorBase == "" {
		return ""
	}

	return fmt.Sprintf("(%s) | ancestors(%s, %d)", revset, fallbackAncestorBase, step)
}

type ancestorsRange struct {
	intStart int
	intEnd   int
	value    int
}

func incrementLargestAncestorsRange(revset string, step int) (string, bool) {
	ranges := findAncestorsRanges(revset)
	if len(ranges) == 0 {
		return "", false
	}

	target := ranges[0]
	for _, candidate := range ranges[1:] {
		if candidate.value > target.value {
			target = candidate
		}
	}

	nextValue := target.value + step
	updated := revset[:target.intStart] + strconv.Itoa(nextValue) + revset[target.intEnd:]
	return updated, true
}

func findAncestorsRanges(revset string) []ancestorsRange {
	const token = "ancestors("

	ranges := make([]ancestorsRange, 0)
	searchFrom := 0
	for {
		idx := strings.Index(revset[searchFrom:], token)
		if idx == -1 {
			break
		}

		start := searchFrom + idx
		openParen := start + len("ancestors")
		closeParen := findMatchingParen(revset, openParen)
		if closeParen == -1 {
			searchFrom = start + len(token)
			continue
		}

		args := revset[openParen+1 : closeParen]
		secondArgStart, secondArgEnd, ok := secondArgRange(args)
		if ok {
			trimmedStart, trimmedEnd := trimRange(args, secondArgStart, secondArgEnd)
			if trimmedStart < trimmedEnd {
				if n, err := strconv.Atoi(args[trimmedStart:trimmedEnd]); err == nil {
					ranges = append(ranges, ancestorsRange{
						intStart: openParen + 1 + trimmedStart,
						intEnd:   openParen + 1 + trimmedEnd,
						value:    n,
					})
				}
			}
		}

		searchFrom = closeParen + 1
	}

	return ranges
}

func findMatchingParen(s string, openParen int) int {
	if openParen < 0 || openParen >= len(s) || s[openParen] != '(' {
		return -1
	}

	depth := 0
	inQuote := byte(0)
	escaped := false

	for i := openParen; i < len(s); i++ {
		ch := s[i]

		if inQuote != 0 {
			if escaped {
				escaped = false
				continue
			}
			if ch == '\\' {
				escaped = true
				continue
			}
			if ch == inQuote {
				inQuote = 0
			}
			continue
		}

		if ch == '\'' || ch == '"' {
			inQuote = ch
			continue
		}

		switch ch {
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				return i
			}
		}
	}

	return -1
}

func secondArgRange(args string) (int, int, bool) {
	depth := 0
	inQuote := byte(0)
	escaped := false
	firstComma := -1

	for i := 0; i < len(args); i++ {
		ch := args[i]

		if inQuote != 0 {
			if escaped {
				escaped = false
				continue
			}
			if ch == '\\' {
				escaped = true
				continue
			}
			if ch == inQuote {
				inQuote = 0
			}
			continue
		}

		if ch == '\'' || ch == '"' {
			inQuote = ch
			continue
		}

		switch ch {
		case '(':
			depth++
		case ')':
			if depth > 0 {
				depth--
			}
		case ',':
			if depth == 0 {
				if firstComma != -1 {
					return 0, 0, false
				}
				firstComma = i
			}
		}
	}

	if firstComma == -1 {
		return 0, 0, false
	}

	return firstComma + 1, len(args), true
}

func trimRange(s string, start int, end int) (int, int) {
	for start < end {
		if s[start] != ' ' && s[start] != '\t' && s[start] != '\n' && s[start] != '\r' {
			break
		}
		start++
	}

	for start < end {
		last := end - 1
		if s[last] != ' ' && s[last] != '\t' && s[last] != '\n' && s[last] != '\r' {
			break
		}
		end--
	}

	return start, end
}

func (m *Model) selectRevision(revision string) int {
	eqFold := func(other string) bool {
		return strings.EqualFold(other, revision)
	}

	idx := slices.IndexFunc(m.rows, func(row parser.Row) bool {
		if revision == "@" {
			return row.Commit.IsWorkingCopy
		}
		return eqFold(row.Commit.GetChangeId()) || eqFold(row.Commit.ChangeId) || eqFold(row.Commit.CommitId)
	})
	return idx
}

func (m *Model) search(startIndex int, backward bool) int {
	if m.quickSearch == "" {
		return m.cursor
	}

	n := len(m.rows)
	for i := range n {
		var c int
		if !backward {
			c = (startIndex + i) % n
		} else {
			// calculate circular index going backwards
			c = (startIndex - i + n) % n
		}
		row := &m.rows[c]
		for _, line := range row.Lines {
			for _, segment := range line.Segments {
				if segment.Text != "" && strings.Contains(strings.ToLower(segment.Text), m.quickSearch) {
					return c
				}
			}
		}
	}
	return m.cursor
}

func (m *Model) CurrentOperation() operations.Operation {
	return m.op.(operations.Operation)
}

func (m *Model) GetCommitIds() []string {
	var commitIds []string
	for _, row := range m.rows {
		commitIds = append(commitIds, row.Commit.CommitId)
	}
	return commitIds
}

func New(c *appContext.MainContext) *Model {
	keymap := config.Current.GetKeyMap()
	m := Model{
		context:       c,
		keymap:        keymap,
		rows:          nil,
		offScreenRows: nil,
		op:            operations.NewDefault(),
		cursor:        0,
		textStyle:     common.DefaultPalette.Get("revisions text"),
		dimmedStyle:   common.DefaultPalette.Get("revisions dimmed"),
		selectedStyle: common.DefaultPalette.Get("revisions selected"),
		matchedStyle:  common.DefaultPalette.Get("revisions matched"),
	}
	m.displayContextRenderer = NewDisplayContextRenderer(m.textStyle, m.dimmedStyle, m.selectedStyle, m.matchedStyle)
	return &m
}

func (m *Model) jumpToParent(revisions jj.SelectedRevisions) {
	immediate, _ := m.context.RunCommandImmediate(jj.GetParent(revisions))
	parentIndex := m.selectRevision(string(immediate))
	if parentIndex != -1 {
		m.SetCursor(parentIndex)
	}
}
