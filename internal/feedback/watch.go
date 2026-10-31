package feedback

import (
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"sort"
	"time"
)

// Check is one CI check as `gh pr checks --json name,bucket,link` prints it.
type Check struct {
	Name   string `json:"name"`
	Bucket string `json:"bucket"`
	Link   string `json:"link"`
}

// ParseChecks reads `gh pr checks` output. gh exits non-zero while checks are
// pending and errors when none exist yet, so anything unreadable is no checks.
func ParseChecks(out []byte) []Check {
	var cs []Check
	if json.Unmarshal(out, &cs) != nil {
		return nil
	}
	return cs
}

// Watcher waits for a PR's CI checks and, optionally, a Copilot review of the
// PR's current head commit.
type Watcher struct {
	Client        *Client
	Repo          string // owner/name, for the first line
	PR            int
	Copilot       bool
	Timeout       time.Duration
	Interval      time.Duration
	NoChecksGrace time.Duration

	Checks func() []Check
	Now    func() time.Time
	Sleep  func(time.Duration)
	Out    io.Writer
}

var copilotLogin = regexp.MustCompile(`(?i)copilot`)

// Run prints one line per event while waiting, then the final block. It
// reports whether it gave up on the timeout.
func (w *Watcher) Run() (timedOut bool, err error) {
	var pr struct {
		Head struct {
			SHA string `json:"sha"`
		} `json:"head"`
	}
	if err := w.Client.REST.Get(w.Client.path("pulls/%d", w.PR), &pr); err != nil {
		return false, fmt.Errorf("gh: cannot read PR #%d: %w", w.PR, err)
	}
	head := pr.Head.SHA
	short := head
	if len(short) > 7 {
		short = short[:7]
	}
	fmt.Fprintf(w.Out, "Watching %s#%d at %s (copilot: %t, timeout: %ds)\n",
		w.Repo, w.PR, short, w.Copilot, int(w.Timeout.Seconds()))

	start := w.Now()
	ciState := "pending"
	copilotState := "not-waited"
	if w.Copilot {
		copilotState = "pending"
	}
	var copilotComments int
	var copilotURL string
	var checks []Check
	seen := map[string]bool{}

	for {
		elapsed := w.Now().Sub(start)

		if ciState == "pending" {
			checks = w.Checks()
			var done []string
			for _, c := range checks {
				if c.Bucket != "pending" {
					done = append(done, fmt.Sprintf("CI %s: %s", c.Name, c.Bucket))
				}
			}
			sort.Strings(done)
			for _, line := range done {
				if !seen[line] {
					fmt.Fprintln(w.Out, line)
					seen[line] = true
				}
			}

			if len(checks) == 0 {
				if elapsed >= w.NoChecksGrace {
					ciState = "none"
				}
			} else if allDone(checks) {
				ciState = "pass"
				if anyFailed(checks) {
					ciState = "fail"
				}
				fmt.Fprintf(w.Out, "CI finished: %s\n", ciState)
			}
		}

		if copilotState == "pending" {
			// Only a review of the current head counts: an older one predates this push.
			if id, url, ok := w.copilotReview(head); ok {
				comments, err := getAll[json.RawMessage](w.Client, w.Client.path("pulls/%d/reviews/%d/comments?per_page=100", w.PR, id))
				if err == nil {
					copilotComments, copilotURL, copilotState = len(comments), url, "reviewed"
					fmt.Fprintf(w.Out, "Copilot reviewed: %d inline comments\n", copilotComments)
				}
			}
		}

		if ciState != "pending" && copilotState != "pending" {
			break
		}
		if elapsed >= w.Timeout {
			timedOut = true
			break
		}
		w.Sleep(w.Interval)
	}

	fmt.Fprintln(w.Out, "---")
	fmt.Fprintf(w.Out, "CI: %s\n", ciState)
	for _, c := range checks {
		if failed(c) {
			fmt.Fprintf(w.Out, "FAILED: %s %s\n", c.Name, c.Link)
		}
	}
	if copilotState == "reviewed" {
		fmt.Fprintf(w.Out, "COPILOT: %d inline comments\n", copilotComments)
		fmt.Fprintf(w.Out, "COPILOT_REVIEW: %s\n", copilotURL)
	} else {
		fmt.Fprintf(w.Out, "COPILOT: %s\n", copilotState)
	}
	return timedOut, nil
}

// copilotReview finds the last Copilot review of head. A read error counts as
// no review yet: the next poll tries again.
func (w *Watcher) copilotReview(head string) (id int64, url string, ok bool) {
	reviews, err := getAll[struct {
		ID       int64  `json:"id"`
		User     user   `json:"user"`
		CommitID string `json:"commit_id"`
		HTMLURL  string `json:"html_url"`
	}](w.Client, w.Client.path("pulls/%d/reviews?per_page=100", w.PR))
	if err != nil {
		return 0, "", false
	}
	for _, r := range reviews {
		if copilotLogin.MatchString(r.User.Login) && r.CommitID == head {
			id, url, ok = r.ID, r.HTMLURL, true
		}
	}
	return id, url, ok
}

func failed(c Check) bool { return c.Bucket == "fail" || c.Bucket == "cancel" }

func allDone(cs []Check) bool {
	for _, c := range cs {
		if c.Bucket == "pending" {
			return false
		}
	}
	return true
}

func anyFailed(cs []Check) bool {
	for _, c := range cs {
		if failed(c) {
			return true
		}
	}
	return false
}
