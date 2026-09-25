package doctor

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
)

// WatchProc is one live `cox watch --epic <dir>` process found on this machine.
type WatchProc struct {
	Pid  int
	Epic string
}

// ListWatchProcs enumerates live `cox watch --epic <dir>` processes via ps (darwin/linux). It is a package var so a
// test injects a fake process table without a real ps. On an unsupported platform or a ps failure it returns nil, so
// the orphan check simply reports nothing rather than failing doctor.
var ListWatchProcs = func() []WatchProc {
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		return nil
	}
	out, ok := probe("ps", "-axww", "-o", "pid=,args=")
	if !ok {
		return nil
	}
	return parseWatchProcs(out)
}

// watchEpicRe pulls the pid and the --epic argument out of a `cox watch ... --epic <dir>` command line. The command must
// name a cox binary running the `watch` subcommand, so an unrelated process that merely mentions "--epic" is ignored.
var (
	watchCmdRe  = regexp.MustCompile(`(^|/)cox\s+watch(\s|$)`)
	watchEpicRe = regexp.MustCompile(`--epic(?:=|\s+)(\S+)`)
)

// parseWatchProcs parses `ps -o pid=,args=` output into the live cox-watch processes. Pure, so a test drives it with
// canned ps text.
func parseWatchProcs(psOut string) []WatchProc {
	var out []WatchProc
	for _, line := range strings.Split(psOut, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		sp := strings.IndexAny(line, " \t")
		if sp < 0 {
			continue
		}
		pid, err := strconv.Atoi(strings.TrimSpace(line[:sp]))
		if err != nil {
			continue
		}
		args := strings.TrimSpace(line[sp:])
		if !watchCmdRe.MatchString(args) {
			continue
		}
		m := watchEpicRe.FindStringSubmatch(args)
		if m == nil {
			continue
		}
		out = append(out, WatchProc{Pid: pid, Epic: m[1]})
	}
	return out
}

// OrphanWatchers returns one ISSUE line per live cox-watch process whose epic dir no longer exists or sits outside every
// known workspace root (B-37: disposable dogfood watchers kept polling temp roots for hours after the worktrees were
// removed). It reads the live process table through ListWatchProcs.
func OrphanWatchers(roots []string) []string {
	return orphanWatchers(roots, ListWatchProcs())
}

// orphanWatchers is the pure core: given the roots and the process table, name every orphan.
func orphanWatchers(roots []string, procs []WatchProc) []string {
	var issues []string
	for _, p := range procs {
		// A relative --epic resolves against the watcher's own cwd, which ps does not report: unverifiable from here,
		// so never an orphan (B-71b; cox watch absolutizes its --epic at parse time, B-71a).
		if p.Epic == "" || !filepath.IsAbs(p.Epic) {
			continue
		}
		reason := ""
		if _, err := os.Stat(p.Epic); err != nil {
			reason = "epic dir no longer exists"
		} else if !underAnyRoot(p.Epic, roots) {
			reason = "not under any known workspace"
		}
		if reason != "" {
			issues = append(issues, fmt.Sprintf("orphan watcher pid %d --epic %s (%s)", p.Pid, p.Epic, reason))
		}
	}
	return issues
}

// underAnyRoot reports whether epic sits at or below one of the roots (path-boundary aware, so /a/bc is not "under" /a/b).
func underAnyRoot(epic string, roots []string) bool {
	e := filepath.Clean(epic)
	for _, r := range roots {
		if r == "" {
			continue
		}
		r = filepath.Clean(r)
		if e == r || strings.HasPrefix(e, r+string(filepath.Separator)) {
			return true
		}
	}
	return false
}
