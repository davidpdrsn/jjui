package bookmarks

import (
	"fmt"
	"slices"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/cellbuf"
	"github.com/idursun/jjui/internal/jj"
	"github.com/idursun/jjui/internal/ui/layout"
	"github.com/idursun/jjui/internal/ui/render"
	"github.com/idursun/jjui/test"
	"github.com/stretchr/testify/assert"
)

func TestDistanceMap(t *testing.T) {
	selectedCommitId := "x"
	changeIds := []string{"a", "x", "b", "c", "d"}
	distanceMap := calcDistanceMap(selectedCommitId, changeIds)
	assert.Equal(t, 0, distanceMap["x"])
	assert.Equal(t, -1, distanceMap["a"])
	assert.Equal(t, 1, distanceMap["b"])
	assert.Equal(t, 2, distanceMap["c"])
	assert.Equal(t, 3, distanceMap["d"])
	assert.Equal(t, 0, distanceMap["nonexistent"])
}

func Test_Sorting_MoveCommands(t *testing.T) {
	items := []item{
		item{name: "move feature", dist: 5, priority: moveCommand},
		item{name: "move main", dist: 1, priority: moveCommand},
		item{name: "move very-old-feature", dist: 15, priority: moveCommand},
		item{name: "move backwards", dist: -2, priority: moveCommand},
	}
	slices.SortFunc(items, itemSorter)
	var sorted []string
	for _, i := range items {
		sorted = append(sorted, i.name)
	}
	assert.Equal(t, []string{"move main", "move feature", "move very-old-feature", "move backwards"}, sorted)
}

func Test_Sorting_MixedCommands(t *testing.T) {
	items := []item{
		item{name: "move very-old-feature", dist: 2, priority: moveCommand},
		item{name: "move main", dist: 0, priority: moveCommand},
		item{name: "delete very-old-feature", dist: 3, priority: deleteCommand},
		item{name: "delete main", dist: 0, priority: deleteCommand},
	}
	slices.SortFunc(items, itemSorter)
	var sorted []string
	for _, i := range items {
		sorted = append(sorted, i.name)
	}
	assert.Equal(t, []string{"move main", "move very-old-feature", "delete main", "delete very-old-feature"}, sorted)
}

// TestBookmarks_ZIndex_RendersAboveMainContent verifies that the bookmarks
// overlay renders at z-index >= render.ZMenuBorder. This ensures the bookmarks
// operations menu renders above the main revision list content.
func TestBookmarks_ZIndex_RendersAboveMainContent(t *testing.T) {
	commandRunner := test.NewTestCommandRunner(t)
	commandRunner.Expect(jj.GitRemoteList()).SetOutput([]byte("origin"))
	commandRunner.Expect(jj.BookmarkListAll()).SetOutput([]byte(""))
	commandRunner.Expect(jj.BookmarkListMovable("abc123")).SetOutput([]byte(""))

	commit := &jj.Commit{ChangeId: "abc123", CommitId: "commit123"}
	op := NewModel(test.NewTestContext(commandRunner), commit, []string{"commit123"})
	test.SimulateModel(op, op.Init())

	dl := render.NewDisplayContext()
	box := layout.Box{R: cellbuf.Rect(0, 0, 100, 40)}
	op.ViewRect(dl, box)

	draws := dl.DrawList()

	for i, draw := range draws {
		msg := fmt.Sprintf("Draw operation %d has z-index %d, expected >= %d. "+
			"Bookmarks overlay must render above main content.",
			i, draw.Z, render.ZMenuBorder)
		assert.GreaterOrEqual(t, draw.Z, render.ZMenuBorder, msg)
	}
}

func TestBookmarks_ListMode_ActivationAndOrdering(t *testing.T) {
	op, commandRunner := newBookmarksModel(t, []byte(`main;.;false;false;false;7
main;origin;true;false;false;7
topic;.;false;false;false;6
feature;origin;true;false;false;5
`), []byte(""))
	defer commandRunner.Verify()

	test.SimulateModel(op, pressRune('l'))

	assert.Equal(t, string("list"), op.categoryFilter)
	items := op.visibleItems()
	assert.Len(t, items, 3)
	assert.Equal(t, "main", items[0].name)
	assert.Equal(t, "topic", items[1].name)
	assert.Equal(t, "feature@origin", items[2].name)
}

func TestBookmarks_ListMode_JKNavigation(t *testing.T) {
	op, commandRunner := newBookmarksModel(t, []byte(`main;.;false;false;false;7
topic;.;false;false;false;6
feature;origin;true;false;false;5
`), []byte(""))
	defer commandRunner.Verify()

	test.SimulateModel(op, pressRune('l'))
	assert.Equal(t, 0, op.cursor)

	test.SimulateModel(op, pressRune('j'))
	assert.Equal(t, 1, op.cursor)

	test.SimulateModel(op, pressRune('k'))
	assert.Equal(t, 0, op.cursor)
}

func TestBookmarks_ListMode_EditWithE(t *testing.T) {
	op, commandRunner := newBookmarksModel(t, []byte(`main;.;false;false;false;7
`), []byte(""))
	commandRunner.Expect(jj.Edit("main", false))
	defer commandRunner.Verify()

	test.SimulateModel(op, tea.Sequence(pressRune('l'), pressRune('e')))
}

func TestBookmarks_ListMode_NewWithN(t *testing.T) {
	op, commandRunner := newBookmarksModel(t, []byte(`main;.;false;false;false;7
feature;origin;true;false;false;5
`), []byte(""))
	commandRunner.Expect(jj.Args("new", "feature@origin"))
	defer commandRunner.Verify()

	test.SimulateModel(op, tea.Sequence(pressRune('l'), pressRune('j'), pressRune('n')))
}

func TestBookmarks_ListMode_RemoteOnlyOneRowPerRemote(t *testing.T) {
	op, commandRunner := newBookmarksModel(t, []byte(`feature;origin;true;false;false;5
feature;upstream;true;false;false;5
`), []byte(""))
	defer commandRunner.Verify()

	test.SimulateModel(op, pressRune('l'))
	items := op.visibleItems()
	assert.Len(t, items, 2)
	assert.Equal(t, "feature@origin", items[0].name)
	assert.Equal(t, "feature@upstream", items[1].name)
}

func TestBookmarks_ListMode_EnterDoesNothing(t *testing.T) {
	op, commandRunner := newBookmarksModel(t, []byte(`main;.;false;false;false;7
`), []byte(""))
	defer commandRunner.Verify()

	test.SimulateModel(op, tea.Sequence(pressRune('l'), test.Press(tea.KeyEnter)))
}

func TestBookmarks_NonListMode_EnterStillApplies(t *testing.T) {
	op, commandRunner := newBookmarksModel(t, []byte(""), []byte(`main;.;false;false;false;5
`))
	commandRunner.Expect(jj.BookmarkMove("abc123", "main"))
	defer commandRunner.Verify()

	test.SimulateModel(op, tea.Sequence(pressRune('m'), test.Press(tea.KeyEnter)))
}

func newBookmarksModel(t *testing.T, listAllOutput []byte, movableOutput []byte) (*Model, *test.CommandRunner) {
	commandRunner := test.NewTestCommandRunner(t)
	commandRunner.Expect(jj.GitRemoteList()).SetOutput([]byte("origin"))
	commandRunner.Expect(jj.BookmarkListAll()).SetOutput(listAllOutput)
	commandRunner.Expect(jj.BookmarkListMovable("abc123")).SetOutput(movableOutput)

	commit := &jj.Commit{ChangeId: "abc123", CommitId: "commit123"}
	op := NewModel(test.NewTestContext(commandRunner), commit, []string{"commit123", "c2", "c3"})
	test.SimulateModel(op, op.Init())
	return op, commandRunner
}

func pressRune(r rune) tea.Cmd {
	return func() tea.Msg {
		return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}}
	}
}
