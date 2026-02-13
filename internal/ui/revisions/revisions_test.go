package revisions

import (
	"errors"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/idursun/jjui/internal/jj"
	"github.com/idursun/jjui/internal/parser"
	"github.com/idursun/jjui/internal/screen"
	"github.com/idursun/jjui/internal/ui/common"
	"github.com/idursun/jjui/internal/ui/intents"
	"github.com/idursun/jjui/test"
	"github.com/stretchr/testify/assert"
)

func TestModel_highlightChanges(t *testing.T) {
	model := Model{
		rows: []parser.Row{
			{Commit: &jj.Commit{ChangeId: "someother"}},
			{Commit: &jj.Commit{ChangeId: "nyqzpsmt"}},
		},
		output: `
Absorbed changes into these revisions:
  nyqzpsmt 8b1e95e3 change third file
Working copy now at: okrwsxvv 5233c94f (empty) (no description set)
Parent commit      : nyqzpsmt 8b1e95e3 change third file
`, err: nil,
	}
	_ = model.highlightChanges()
	assert.False(t, model.rows[0].IsAffected)
	assert.True(t, model.rows[1].IsAffected)
}

var rows = []parser.Row{
	{
		Commit: &jj.Commit{ChangeId: "a", CommitId: "123456789abc"},
		Lines: []*parser.GraphRowLine{
			{
				Gutter:   parser.GraphGutter{Segments: []*screen.Segment{{Text: "|"}}},
				Segments: []*screen.Segment{{Text: "a"}},
				Flags:    parser.Revision,
			},
		},
	},
	{
		Commit: &jj.Commit{ChangeId: "b", CommitId: "9"},
		Lines: []*parser.GraphRowLine{
			{
				Gutter:   parser.GraphGutter{Segments: []*screen.Segment{{Text: "|"}}},
				Segments: []*screen.Segment{{Text: "b"}},
				Flags:    parser.Revision,
			},
		},
	},
}

func TestModel_Navigate(t *testing.T) {
	ctx := test.NewTestContext(test.NewTestCommandRunner(t))
	model := New(ctx)
	model.updateGraphRows(rows, "a")

	test.SimulateModel(model, model.Update(intents.Navigate{Delta: 1}))
	assert.Equal(t, "b", model.SelectedRevision().ChangeId)
	test.SimulateModel(model, model.Update(intents.Navigate{Delta: -1}))
	assert.Equal(t, "a", model.SelectedRevision().ChangeId)
}

func TestModel_OperationIntents(t *testing.T) {
	tests := []struct {
		name     string
		intent   intents.Intent
		expected string
	}{
		{
			name:     "abandon",
			intent:   intents.StartAbandon{},
			expected: "abandon",
		},
		{
			name:     "rebase",
			intent:   intents.StartRebase{},
			expected: "rebase",
		},
		{
			name:     "duplicate",
			intent:   intents.StartDuplicate{},
			expected: "duplicate",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ctx := test.NewTestContext(test.NewTestCommandRunner(t))

			model := New(ctx)
			model.updateGraphRows(rows, "a")
			test.SimulateModel(model, model.Update(tc.intent))
			assert.False(t, model.InNormalMode())
			rendered := test.RenderImmediate(model, 100, 50)
			assert.Contains(t, rendered, tc.expected)
		})
	}
}

func TestModel_CopyChangeID(t *testing.T) {
	commandRunner := test.NewTestCommandRunner(t)
	commandRunner.Expect(jj.GetFullIdsFromRevset("a")).SetOutput([]byte("vpumnpzu123\n"))
	defer commandRunner.Verify()

	ctx := test.NewTestContext(commandRunner)
	model := New(ctx)
	model.updateGraphRows(rows, "a")

	oldWriteClipboard := writeClipboard
	t.Cleanup(func() {
		writeClipboard = oldWriteClipboard
	})

	var copied string
	writeClipboard = func(value string) error {
		copied = value
		return nil
	}

	var flashMsg intents.AddMessage
	test.SimulateModel(model, test.Type("C"), func(msg tea.Msg) {
		if got, ok := msg.(intents.AddMessage); ok {
			flashMsg = got
		}
	})

	assert.Equal(t, "vpumnpzu1", copied)
	assert.Equal(t, "Copied change id: vpumnpzu1", flashMsg.Text)
	assert.NoError(t, flashMsg.Err)
}

func TestModel_CopyChangeID_ShowsErrorOnClipboardFailure(t *testing.T) {
	commandRunner := test.NewTestCommandRunner(t)
	commandRunner.Expect(jj.GetFullIdsFromRevset("a")).SetOutput([]byte("vpumnpzu\n"))
	defer commandRunner.Verify()

	ctx := test.NewTestContext(commandRunner)
	model := New(ctx)
	model.updateGraphRows(rows, "a")

	oldWriteClipboard := writeClipboard
	t.Cleanup(func() {
		writeClipboard = oldWriteClipboard
	})

	copyErr := errors.New("clipboard unavailable")
	writeClipboard = func(string) error {
		return copyErr
	}

	var flashMsg intents.AddMessage
	test.SimulateModel(model, test.Type("C"), func(msg tea.Msg) {
		if got, ok := msg.(intents.AddMessage); ok {
			flashMsg = got
		}
	})

	assert.Equal(t, "", flashMsg.Text)
	assert.Equal(t, copyErr, flashMsg.Err)
}

func TestModel_NavigateBottom_ExpandsLargestAncestorsRange(t *testing.T) {
	ctx := test.NewTestContext(test.NewTestCommandRunner(t))
	ctx.CurrentRevset = "present(@) | ancestors(immutable_heads().., 2) | present(trunk()) | ancestors(trunk(), 20)"

	model := New(ctx)
	model.updateGraphRows(rows, "b")
	model.SetCursor(len(rows) - 1)

	cmd := model.navigate(intents.Navigate{Delta: 1})
	if assert.NotNil(t, cmd) {
		msg := cmd()
		updateMsg, ok := msg.(common.UpdateRevSetMsg)
		if assert.True(t, ok) {
			assert.Equal(t, "present(@) | ancestors(immutable_heads().., 2) | present(trunk()) | ancestors(trunk(), 70)", string(updateMsg))
		}
	}
}

func TestModel_NavigateBottom_FallsBackToAppendingAncestors(t *testing.T) {
	ctx := test.NewTestContext(test.NewTestCommandRunner(t))
	ctx.CurrentRevset = "present(@)"

	model := New(ctx)
	model.updateGraphRows(rows, "b")
	model.SetCursor(len(rows) - 1)

	cmd := model.navigate(intents.Navigate{Delta: 1})
	if assert.NotNil(t, cmd) {
		msg := cmd()
		updateMsg, ok := msg.(common.UpdateRevSetMsg)
		if assert.True(t, ok) {
			assert.Equal(t, "(present(@)) | ancestors(b, 50)", string(updateMsg))
		}
	}
}

func TestIncrementLargestAncestorsRange_HandlesNestedArgs(t *testing.T) {
	revset := `present(@) | ancestors(description(glob:"foo,bar"), 3) | ancestors(trunk(), 20)`

	updated, ok := incrementLargestAncestorsRange(revset, 50)
	if assert.True(t, ok) {
		assert.Equal(t, `present(@) | ancestors(description(glob:"foo,bar"), 3) | ancestors(trunk(), 70)`, updated)
	}
}

func TestModel_NavigateTargetBottom_DoesNotExpandRevset(t *testing.T) {
	ctx := test.NewTestContext(test.NewTestCommandRunner(t))
	ctx.CurrentRevset = "present(@) | ancestors(trunk(), 20)"

	model := New(ctx)
	model.updateGraphRows(rows, "a")

	cmd := model.navigate(intents.Navigate{Target: intents.TargetBottom})
	assert.Equal(t, len(rows)-1, model.Cursor())

	if cmd != nil {
		msg := cmd()
		_, isRevsetUpdate := msg.(common.UpdateRevSetMsg)
		assert.False(t, isRevsetUpdate)
	}
}
