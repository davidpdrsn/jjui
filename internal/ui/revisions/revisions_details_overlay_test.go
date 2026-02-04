package revisions

import (
	"testing"

	"github.com/idursun/jjui/internal/jj"
	"github.com/idursun/jjui/internal/parser"
	"github.com/idursun/jjui/internal/ui/operations/details"
	"github.com/idursun/jjui/test"
	"github.com/stretchr/testify/assert"
)

func TestDetailsOverlayPreventsRevisionNavigation(t *testing.T) {
	ctx := test.NewTestContext(test.NewTestCommandRunner(t))
	model := New(ctx)
	rows := []parser.Row{
		{Commit: &jj.Commit{ChangeId: "one", CommitId: "1"}},
		{Commit: &jj.Commit{ChangeId: "two", CommitId: "2"}},
	}
	model.updateGraphRows(rows, rows[0].Commit.GetChangeId())
	model.op = details.NewOperation(ctx, rows[0].Commit)

	initialCursor := model.Cursor()
	test.SimulateModel(model, test.Type("j"))

	assert.Equal(t, initialCursor, model.Cursor())
}
