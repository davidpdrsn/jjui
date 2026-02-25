package ai_implement

import (
	"path/filepath"
	"testing"

	"github.com/idursun/jjui/internal/jj"
	"github.com/idursun/jjui/test"
)

func TestCommandForRevision_PrefersVmuxWhenBothSet(t *testing.T) {
	t.Setenv("VMUX", "1")
	t.Setenv("VMUX_TERMINAL_ID", "6")
	t.Setenv("TMUX", "1")

	commandRunner := test.NewTestCommandRunner(t)
	defer commandRunner.Verify()

	context := test.NewTestContext(commandRunner)
	context.Location = t.TempDir()
	commit := &jj.Commit{ChangeId: "abcdef"}
	op := NewOperation(context, jj.NewSelectedRevisions(commit))
	op.vmuxAvailable = func() bool { return true }

	addArgs := jj.AiImplementAdd(commit.GetChangeId(), false, false, modelCodex53)
	expected := append([]string{"vmux"}, op.vmuxArgs(commit, addArgs)...)
	commandRunner.Expect(expected)

	test.SimulateModel(op, op.commandForRevision(commit))
}

func TestCommandForRevision_VmuxPlanUsesFocus(t *testing.T) {
	t.Setenv("VMUX", "1")
	t.Setenv("VMUX_TERMINAL_ID", "6")
	t.Setenv("TMUX", "")

	commandRunner := test.NewTestCommandRunner(t)
	defer commandRunner.Verify()

	context := test.NewTestContext(commandRunner)
	context.Location = t.TempDir()
	commit := &jj.Commit{ChangeId: "abcdef"}
	op := NewOperation(context, jj.NewSelectedRevisions(commit))
	op.vmuxAvailable = func() bool { return true }
	op.plan = true

	addArgs := jj.AiImplementAdd(commit.GetChangeId(), false, true, modelCodex53)
	expected := []string{
		"vmux",
		"terminal", "add", "--current-project", "--focus",
		"--cwd", context.Location,
		filepath.Base(context.Location) + "-abcde",
		"--", jj.AiImplementProgram,
	}
	expected = append(expected, addArgs...)
	commandRunner.Expect(expected)

	test.SimulateModel(op, op.commandForRevision(commit))
}

func TestCommandForRevision_FallsBackWhenVmuxUnavailable(t *testing.T) {
	t.Setenv("VMUX", "1")
	t.Setenv("VMUX_TERMINAL_ID", "6")
	t.Setenv("TMUX", "")

	commandRunner := test.NewTestCommandRunner(t)
	defer commandRunner.Verify()

	context := test.NewTestContext(commandRunner)
	commit := &jj.Commit{ChangeId: "abcdef"}
	op := NewOperation(context, jj.NewSelectedRevisions(commit))
	op.vmuxAvailable = func() bool { return false }

	addArgs := jj.AiImplementAdd(commit.GetChangeId(), false, false, modelCodex53)
	commandRunner.Expect(append([]string{jj.AiImplementProgram}, addArgs...))

	test.SimulateModel(op, op.commandForRevision(commit))
}

func TestCommandForRevision_UsesTmuxWhenOnlyTmuxSet(t *testing.T) {
	t.Setenv("VMUX", "")
	t.Setenv("TMUX", "1")

	commandRunner := test.NewTestCommandRunner(t)
	defer commandRunner.Verify()

	context := test.NewTestContext(commandRunner)
	context.Location = t.TempDir()
	commit := &jj.Commit{ChangeId: "abcdef"}
	op := NewOperation(context, jj.NewSelectedRevisions(commit))

	addArgs := jj.AiImplementAdd(commit.GetChangeId(), false, false, modelCodex53)
	expected := append([]string{"tmux"}, op.tmuxArgs(commit, addArgs)...)
	commandRunner.Expect(expected)

	test.SimulateModel(op, op.commandForRevision(commit))
}

func TestCommandForRevision_UsesDirectCommandWithoutMux(t *testing.T) {
	t.Setenv("VMUX", "")
	t.Setenv("TMUX", "")

	commandRunner := test.NewTestCommandRunner(t)
	defer commandRunner.Verify()

	context := test.NewTestContext(commandRunner)
	commit := &jj.Commit{ChangeId: "abcdef"}
	op := NewOperation(context, jj.NewSelectedRevisions(commit))

	addArgs := jj.AiImplementAdd(commit.GetChangeId(), false, false, modelCodex53)
	commandRunner.Expect(append([]string{jj.AiImplementProgram}, addArgs...))

	test.SimulateModel(op, op.commandForRevision(commit))
}
