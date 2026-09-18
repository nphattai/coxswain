package report

import "testing"

const v2Report = `| claim | evidence | severity | proposal |
|---|---|---|---|
| the thing breaks | cox/a.go:1@abc1234 | epic-blocking | fix it |
`

const v3Report = `---
recommendation: adopt with the guard below
assumptions:
  - the queue is single-writer
  - reset is monotonic
checks_required:
  - confirm the lock is held
conflicts_with:
  - 0013
---
# Arena adversary

| claim | evidence | tier | severity | confidence | check | proposal |
|---|---|---|---|---|---|---|
| the lock is dropped early | cox/lock.go:42@abc1234 | 2 | epic-blocking | 80 | go test ./internal/lock/ | hold it to return |
| a doc says otherwise | cox/README.md:3@abc1234 | 4 | minor | 30 | | note it |
`

func TestParseV2(t *testing.T) {
	r := Parse(v2Report)
	if r.Version != V2 {
		t.Fatalf("version = %q, want v2", r.Version)
	}
	if len(r.Claims) != 1 {
		t.Fatalf("claims = %d, want 1", len(r.Claims))
	}
	c := r.Claims[0]
	if c.Severity != "epic-blocking" || c.Proposal != "fix it" {
		t.Errorf("claim mis-parsed: %+v", c)
	}
	if c.Tier != "" || c.Confidence != "" || c.Check != "" {
		t.Errorf("v2 claim should have empty tier/confidence/check, got %+v", c)
	}
}

func TestParseV3(t *testing.T) {
	r := Parse(v3Report)
	if r.Version != V3 {
		t.Fatalf("version = %q, want v3", r.Version)
	}
	if r.Recommendation != "adopt with the guard below" {
		t.Errorf("recommendation = %q", r.Recommendation)
	}
	if len(r.Assumptions) != 2 || r.Assumptions[1] != "reset is monotonic" {
		t.Errorf("assumptions = %v", r.Assumptions)
	}
	if len(r.ChecksRequired) != 1 || r.ChecksRequired[0] != "confirm the lock is held" {
		t.Errorf("checks_required = %v", r.ChecksRequired)
	}
	if len(r.ConflictsWith) != 1 || r.ConflictsWith[0] != "0013" {
		t.Errorf("conflicts_with = %v", r.ConflictsWith)
	}
	if len(r.Claims) != 2 {
		t.Fatalf("claims = %d, want 2", len(r.Claims))
	}
	c := r.Claims[0]
	if c.Tier != "2" || c.Severity != "epic-blocking" || c.Confidence != "80" {
		t.Errorf("claim0 mis-parsed: %+v", c)
	}
	if c.Check != "go test ./internal/lock/" || c.Proposal != "hold it to return" {
		t.Errorf("claim0 check/proposal mis-parsed: %+v", c)
	}
	if r.Claims[1].Check != "" {
		t.Errorf("claim1 check should be empty, got %q", r.Claims[1].Check)
	}
}

// A v3 table with no frontmatter is still v3 by its tier column.
func TestParseV3ByColumn(t *testing.T) {
	const noFM = `| claim | evidence | tier | severity | confidence | check | proposal |
|---|---|---|---|---|---|---|
| x | cox/a.go:1@abc1234 | 3 | minor | 50 | | y |
`
	r := Parse(noFM)
	if r.Version != V3 {
		t.Fatalf("version = %q, want v3 (tier column present)", r.Version)
	}
	if r.Claims[0].Tier != "3" {
		t.Errorf("tier = %q, want 3", r.Claims[0].Tier)
	}
}

func TestParseNoTable(t *testing.T) {
	r := Parse("# nothing here\n\njust prose\n")
	if len(r.Claims) != 0 {
		t.Errorf("claims = %d, want 0", len(r.Claims))
	}
	if r.Version != V2 {
		t.Errorf("version = %q, want v2 (no frontmatter, no tier)", r.Version)
	}
}
