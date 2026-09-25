package inbox

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// The ring state records how many times an unhandled steer has been re-rung and when, so the watcher's re-ring ladder
// (v1 watch.sh) is durable across watcher restarts. Rows are tab-separated `key<TAB>count<TAB>ts`; a numeric count row
// is a ring and the special count `escalated` marks a steer whose ladder is exhausted. (A legacy `*story*` row with
// count `interrupted`, from the retired runaway interrupt, is non-numeric and ignored.)
const ringFile = ".ring-state"

// ringEscalated is the non-numeric row marker.
const ringEscalated = "escalated"

// RingStatus is the current ladder state for one record key.
type RingStatus struct {
	Count     int   // number of numeric ring rows
	LastTS    int64 // ts of the last ring (unix seconds), 0 if never
	Escalated bool  // an escalated row exists
}

// Ring reports the ladder status of a record key within a story inbox dir.
func Ring(inboxDir, key string) (RingStatus, error) {
	rows, err := ringRows(inboxDir)
	if err != nil {
		return RingStatus{}, err
	}
	var st RingStatus
	for _, r := range rows {
		if r.key != key {
			continue
		}
		if r.val == ringEscalated {
			st.Escalated = true
			continue
		}
		if n, err := strconv.Atoi(r.val); err == nil {
			st.Count = n
			st.LastTS = r.ts
		}
	}
	return st, nil
}

// BumpRing appends a numeric ring row (count = previous+1) at ts and returns the new count.
func BumpRing(inboxDir, key string, ts int64) (int, error) {
	st, err := Ring(inboxDir, key)
	if err != nil {
		return 0, err
	}
	n := st.Count + 1
	return n, appendRow(inboxDir, key, strconv.Itoa(n), ts)
}

// MarkEscalated writes the escalated row for a key (idempotent: a caller checks RingStatus.Escalated first).
func MarkEscalated(inboxDir, key string, ts int64) error {
	return appendRow(inboxDir, key, ringEscalated, ts)
}

type ringRow struct {
	key string
	val string
	ts  int64
}

func ringRows(inboxDir string) ([]ringRow, error) {
	f, err := os.Open(filepath.Join(inboxDir, ringFile))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read ring state: %w", err)
	}
	defer f.Close()
	var rows []ringRow
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		parts := strings.SplitN(sc.Text(), "\t", 3)
		if len(parts) < 2 {
			continue
		}
		var ts int64
		if len(parts) == 3 {
			ts, _ = strconv.ParseInt(parts[2], 10, 64)
		}
		rows = append(rows, ringRow{key: parts[0], val: parts[1], ts: ts})
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("scan ring state: %w", err)
	}
	return rows, nil
}

func appendRow(inboxDir, key, val string, ts int64) error {
	if err := os.MkdirAll(inboxDir, 0o755); err != nil {
		return fmt.Errorf("create inbox dir: %w", err)
	}
	f, err := os.OpenFile(filepath.Join(inboxDir, ringFile), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("open ring state: %w", err)
	}
	defer f.Close()
	if _, err := fmt.Fprintf(f, "%s\t%s\t%d\n", key, val, ts); err != nil {
		return fmt.Errorf("write ring state: %w", err)
	}
	return nil
}
