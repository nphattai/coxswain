package github

import (
	"errors"
	"strings"
	"testing"

	"github.com/nphattai/coxswain/internal/adapter/forge"
)

var _ forge.Forge = New("")

func TestPRParse(t *testing.T) {
	c := New("/repo")
	c.run = func(dir string, args ...string) ([]byte, error) {
		return []byte(`{"number":42,"headRefOid":"deadbeef","headRefName":"story/x","baseRefName":"epic/demo","state":"OPEN"}`), nil
	}
	pr, err := c.PR("story/x")
	if err != nil {
		t.Fatal(err)
	}
	if pr.Number != 42 || pr.Head != "deadbeef" || pr.State != "open" {
		t.Fatalf("got %+v", pr)
	}
}

func TestPRErrorSurfaces(t *testing.T) {
	c := New("/repo")
	c.run = func(dir string, args ...string) ([]byte, error) { return nil, errors.New("gh: no pr") }
	if _, err := c.PR("story/x"); err == nil {
		t.Fatal("gh error must surface")
	}
}

func TestChecksMapBuckets(t *testing.T) {
	c := New("/repo")
	c.run = func(dir string, args ...string) ([]byte, error) {
		return []byte(`[{"name":"build","state":"SUCCESS","bucket":"pass"},{"name":"e2e","state":"PENDING","bucket":"pending"}]`), nil
	}
	checks, err := c.Checks(forge.PR{Number: 1})
	if err != nil {
		t.Fatal(err)
	}
	if checks[0].Conclusion != "success" || checks[0].Status != "completed" {
		t.Errorf("pass bucket wrong: %+v", checks[0])
	}
	if checks[1].Status != "in_progress" {
		t.Errorf("pending bucket wrong: %+v", checks[1])
	}
}

func TestCommentsFoldReviews(t *testing.T) {
	c := New("/repo")
	c.run = func(dir string, args ...string) ([]byte, error) {
		return []byte(`{"comments":[{"author":{"login":"bot"},"body":"hi"}],"reviews":[{"author":{"login":"rev"},"body":"nope","state":"CHANGES_REQUESTED"}]}`), nil
	}
	cs, err := c.Comments(forge.PR{Number: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(cs) != 2 || !strings.Contains(cs[1].Body, "Changes Requested") {
		t.Fatalf("changes-requested review must be flagged: %+v", cs)
	}
}
