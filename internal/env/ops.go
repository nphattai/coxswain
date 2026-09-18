// Package env owns a story's environment: it generates the env file completely, validates it, and publishes it
// atomically (tmp + rename, F13); it records port/db/sim ownership in .cox/resources.json and clears ownership only
// after an external delete is confirmed (F03/F04). Every external operation (database, simulator, process) returns a
// real error - never the swallowed `return 0` that made v1 db_drop report success on failure. Those operations sit
// behind the Ops interface so the atomic-publish, ownership and snapshot logic is unit-tested without postgres or xcrun.
package env

import (
	"fmt"
	"os/exec"
	"strings"
	"syscall"
)

// Ops is every external side effect env performs. The real implementation shells out; tests use a fake to drive the
// failure paths (missing snapshot, rename failure, foreign pid) the report demands be exercised independently.
type Ops interface {
	// DBExists reports whether a database exists. An error means the check itself failed (never treated as "absent").
	DBExists(name string) (bool, error)
	// DBCreate creates name from a template database. An error is returned as-is.
	DBCreate(name, template string) error
	// DBDrop drops name. It returns the real outcome: a failed drop is an error (fix for F04, where v1 returned 0).
	DBDrop(name string) error
	// DBRename renames a database. Used by snapshot promotion; a failed rename is an error so promote can recover.
	DBRename(from, to string) error
	// SimClone clones a base simulator and returns the new udid.
	SimClone(base, name string) (string, error)
	// SimDelete shuts down and deletes a simulator by udid; a failed delete is an error.
	SimDelete(udid string) error
	// PortOwner returns the pid and command line of the process listening on port, or ok=false when the port is free.
	PortOwner(port int) (pid int, cmd string, ok bool, err error)
	// Signal sends a signal to a pid (0 to probe liveness). A nil error means the process exists / the signal was sent.
	Signal(pid int, sig syscall.Signal) error
}

// realOps shells out to psql (via docker exec), xcrun simctl, and lsof, matching v1's bin/lib.sh and bin/local-env.sh.
type realOps struct {
	PGContainer string
	PGUser      string
}

// RealOps returns the production Ops. Container/user default to v1's values.
func RealOps() Ops {
	return &realOps{PGContainer: "crewkit-postgres", PGUser: "dev"}
}

func (o *realOps) psql(args ...string) ([]byte, error) {
	full := append([]string{"exec", o.PGContainer, "psql", "-U", o.PGUser, "-d", "postgres", "-v", "ON_ERROR_STOP=1", "-tA"}, args...)
	out, err := exec.Command("docker", full...).CombinedOutput()
	if err != nil {
		return out, fmt.Errorf("psql %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return out, nil
}

func (o *realOps) DBExists(name string) (bool, error) {
	out, err := o.psql("-c", fmt.Sprintf("select 1 from pg_database where datname='%s'", name))
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(string(out)) == "1", nil
}

func (o *realOps) DBCreate(name, template string) error {
	if template == "" {
		template = "template0"
	}
	_, err := o.psql("-c", fmt.Sprintf(`create database "%s" template "%s"`, name, template))
	return err
}

// DBDrop drops the database and reports the real result. Unlike v1 db_drop (which returned 0 unconditionally, F04), a
// failed drop is surfaced so the caller keeps ownership instead of clearing it on a phantom success.
func (o *realOps) DBDrop(name string) error {
	_, err := o.psql("-c", fmt.Sprintf(`drop database if exists "%s" with (force)`, name))
	return err
}

func (o *realOps) DBRename(from, to string) error {
	_, err := o.psql("-c", fmt.Sprintf(`alter database "%s" rename to "%s"`, from, to))
	return err
}

func (o *realOps) SimClone(base, name string) (string, error) {
	out, err := exec.Command("xcrun", "simctl", "clone", base, name).Output()
	if err != nil {
		return "", fmt.Errorf("simctl clone %s: %w", base, err)
	}
	udid := strings.TrimSpace(string(out))
	if udid == "" {
		return "", fmt.Errorf("simctl clone %s: empty udid", base)
	}
	return udid, nil
}

func (o *realOps) SimDelete(udid string) error {
	_ = exec.Command("xcrun", "simctl", "shutdown", udid).Run()
	if out, err := exec.Command("xcrun", "simctl", "delete", udid).CombinedOutput(); err != nil {
		return fmt.Errorf("simctl delete %s: %w: %s", udid, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// PortOwner asks lsof who is listening on a TCP port. No listener is ok=false with no error; an lsof failure other than
// "nothing found" is a real error so the caller does not mistake an unreadable port for a free one.
func (o *realOps) PortOwner(port int) (int, string, bool, error) {
	out, err := exec.Command("lsof", "-nP", "-iTCP:"+itoa(port), "-sTCP:LISTEN", "-Fpc").Output()
	if err != nil {
		// lsof exits 1 when nothing matches; that is "free", not an error.
		if ee, ok := err.(*exec.ExitError); ok && ee.ExitCode() == 1 {
			return 0, "", false, nil
		}
		return 0, "", false, fmt.Errorf("lsof :%d: %w", port, err)
	}
	pid, cmd := parseLsofFp(string(out))
	if pid == 0 {
		return 0, "", false, nil
	}
	return pid, cmd, true, nil
}

func (o *realOps) Signal(pid int, sig syscall.Signal) error {
	return syscall.Kill(pid, sig)
}

// parseLsofFp reads lsof -F output (each field on its own line prefixed by a letter: p<pid>, c<command>).
func parseLsofFp(out string) (int, string) {
	var pid int
	var cmd string
	for _, line := range strings.Split(out, "\n") {
		if line == "" {
			continue
		}
		switch line[0] {
		case 'p':
			pid = atoiSafe(line[1:])
		case 'c':
			cmd = line[1:]
		}
	}
	return pid, cmd
}
