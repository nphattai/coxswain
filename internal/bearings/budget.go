package bearings

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"syscall"

	"github.com/nphattai/coxswain/internal/workspace"
)

// budgetRe is firstmate's exact format: one positive decimal integer, no sign, no leading zero, one newline
// (bin/fm-startup-memory-budget-lib.sh fm_startup_memory_budget_file_valid).
var budgetRe = regexp.MustCompile(`^[1-9][0-9]*\n$`)

// BudgetError is a rejected budget setting or memory file. It is never replaced by a default.
type BudgetError struct{ Reason string }

func (e *BudgetError) Error() string { return "startup-memory-budget: " + e.Reason }

func budgetFail(format string, a ...any) error {
	return &BudgetError{Reason: fmt.Sprintf(format, a...)}
}

// configDirSafe rejects a symlinked or non-directory cox/.
func configDirSafe(dir string) error {
	fi, err := os.Lstat(dir)
	if err != nil {
		return budgetFail("config directory is not a directory")
	}
	if fi.Mode()&os.ModeSymlink != 0 {
		return budgetFail("config directory is symlinked")
	}
	if !fi.IsDir() {
		return budgetFail("config directory is not a directory")
	}
	return nil
}

// budgetFileValid reads a regular, single-linked file holding exactly one positive value and one newline.
func budgetFileValid(path string) (int, error) {
	fi, err := os.Lstat(path)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return 0, budgetFail("file is absent")
	case err != nil:
		return 0, budgetFail("could not inspect file")
	case fi.Mode()&os.ModeSymlink != 0:
		return 0, budgetFail("file is symlinked")
	case !fi.Mode().IsRegular():
		return 0, budgetFail("file is not a regular file")
	}
	if st, ok := fi.Sys().(*syscall.Stat_t); !ok || st.Nlink != 1 {
		return 0, budgetFail("file is hardlinked")
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return 0, budgetFail("could not read file")
	}
	if !budgetRe.Match(b) {
		return 0, budgetFail("file must contain exactly one positive decimal integer followed by one newline")
	}
	n, err := strconv.Atoi(string(b[:len(b)-1]))
	if err != nil {
		return 0, budgetFail("value is out of range")
	}
	return n, nil
}

// ReadBudget validates and returns the effective allowance from cox/notes-budget. An absent file is an error here;
// MaterializeBudget owns the default.
func ReadBudget(ws string) (int, error) {
	dir := filepath.Join(ws, workspace.ControlDir)
	if err := configDirSafe(dir); err != nil {
		return 0, err
	}
	return budgetFileValid(filepath.Join(ws, workspace.NotesBudgetPath()))
}

// MaterializeBudget atomically publishes the visible default (7500) only when cox/notes-budget is absent. A concurrent
// valid creator is accepted; an unsafe or malformed existing file is rejected, never replaced
// (fm_startup_memory_budget_materialize).
func MaterializeBudget(ws string) error {
	dir := filepath.Join(ws, workspace.ControlDir)
	if _, err := os.Lstat(dir); errors.Is(err, os.ErrNotExist) {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return budgetFail("could not create config directory")
		}
	}
	if err := configDirSafe(dir); err != nil {
		return err
	}
	path := filepath.Join(ws, workspace.NotesBudgetPath())
	if _, err := os.Lstat(path); err == nil {
		_, err := ReadBudget(ws)
		return err
	}
	tmp, err := os.CreateTemp(dir, ".notes-budget.")
	if err != nil {
		return budgetFail("could not create default temporary file")
	}
	defer os.Remove(tmp.Name())
	_, werr := fmt.Fprintf(tmp, "%d\n", workspace.DefaultNotesBudget)
	if cerr := tmp.Close(); werr != nil || cerr != nil {
		return budgetFail("could not write default value")
	}
	// link(2) is a no-clobber publication; removing the temporary name leaves the file with exactly one link.
	_ = os.Link(tmp.Name(), path)
	_ = os.Remove(tmp.Name())
	_, err = ReadBudget(ws)
	return err
}

// measure returns a memory file's estimate and presence. A present memory file must be an ordinary regular file, so a
// measurement never follows a symlink or reads a special file.
func measure(path string) (tokens int, present bool, err error) {
	fi, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return 0, false, nil
	}
	if err != nil || !fi.Mode().IsRegular() {
		return 0, false, budgetFail("memory file is not an ordinary regular file: %s", path)
	}
	return Estimate(int(fi.Size())), true, nil
}

// Budget reports the three memory files against this workspace's own allowance (never a fleet total), materializing
// the default setting first when it is absent. The cold archive is never counted.
func Budget(ws string) (BudgetReport, error) {
	if err := MaterializeBudget(ws); err != nil {
		return BudgetReport{}, err
	}
	budget, err := ReadBudget(ws)
	if err != nil {
		return BudgetReport{}, err
	}
	return measureAll(ws, budget)
}

func measureAll(ws string, budget int) (BudgetReport, error) {
	r := BudgetReport{Budget: budget, Files: map[string]int{}}
	for _, f := range workspace.NotesFiles {
		rel := workspace.NotesPath(f)
		n, present, err := measure(filepath.Join(ws, rel))
		if err != nil {
			return BudgetReport{}, err
		}
		if present {
			r.Files[rel] = n
			r.Total += n
		}
	}
	r.Status = "within-budget"
	if r.Total > budget {
		r.Status = "over-budget"
	}
	return r, nil
}
