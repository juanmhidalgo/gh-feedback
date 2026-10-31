package feedback

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func refs(cs []Comment) string {
	var ids []string
	for _, c := range cs {
		ids = append(ids, c.RefID)
	}
	return strings.Join(ids, ",")
}

func TestFetchFiltersResolvedThreadsAcrossPages(t *testing.T) {
	c := newFake(t, defaultFake())
	res, err := c.Fetch(FetchOptions{PR: 7})
	if err != nil {
		t.Fatal(err)
	}
	// The resolved thread on page 2 and its reply are left out.
	if got := refs(res.Comments); got != "review_comment:101,review_comment:301,review_comment:302" {
		t.Fatalf("comments = %s", got)
	}
	t3 := res.Comments[1]
	if !*t3.Outdated {
		t.Error("thread on page 2 lost its outdated flag")
	}
	if *t3.ThreadComments != 2 || *t3.ThreadID != "T3" {
		t.Errorf("thread fields = %v %v, want T3 with 2 comments", *t3.ThreadID, *t3.ThreadComments)
	}
	if *t3.Line != 9 {
		t.Errorf("line = %d, want original_line 9", *t3.Line)
	}
	if !res.Comments[0].IsBot {
		t.Error("Copilot (type Bot) is not a bot")
	}
	if res.Stats.FilteredOut.Resolved != 2 || res.Stats.Total != 3 {
		t.Errorf("stats = %+v", res.Stats)
	}
}

func TestFetchIncludeResolvedKeepsAllAndKnowsHumans(t *testing.T) {
	c := newFake(t, defaultFake())
	res, err := c.Fetch(FetchOptions{PR: 7, IncludeResolved: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Comments) != 5 {
		t.Fatalf("got %d comments across both REST pages, want 5", len(res.Comments))
	}
	for _, cm := range res.Comments {
		if cm.Author == "abbott" && cm.IsBot {
			t.Error("a human named abbott is a bot")
		}
	}
}

func TestFetchBotsOnlyAndTruncation(t *testing.T) {
	f := defaultFake()
	f.rest["/repos/o/r/issues/7/comments"] = []string{
		`[{"id":9,"user":{"login":"coderabbitai","type":"User"},"body":"ñandú<b>&x"}]`,
	}
	f.rest["/repos/o/r/pulls/7/reviews"] = []string{
		`[{"id":1,"user":{"login":"owner"},"body":"","state":"COMMENTED"},
		  {"id":2,"user":{"login":"owner"},"body":"","state":"APPROVED"}]`,
	}
	c := newFake(t, f)
	res, err := c.Fetch(FetchOptions{PR: 7, BotsOnly: true, MaxBody: 3})
	if err != nil {
		t.Fatal(err)
	}
	if got := refs(res.Comments); got != "issue_comment:9,review_comment:101,review_comment:301" {
		t.Fatalf("comments = %s", got)
	}
	if res.Comments[0].Body != "ñan... [truncated]" {
		t.Errorf("body = %q, want truncated by characters", res.Comments[0].Body)
	}

	all, err := c.Fetch(FetchOptions{PR: 7, IncludeResolved: true})
	if err != nil {
		t.Fatal(err)
	}
	// An empty COMMENTED review is noise; an empty approval is not.
	if !strings.Contains(refs(all.Comments), "review:2") || strings.Contains(refs(all.Comments), "review:1") {
		t.Errorf("reviews = %s", refs(all.Comments))
	}
	b, _ := Encode(all)
	if !strings.Contains(string(b), "<b>&x") {
		t.Error("JSON escapes < and & in bodies")
	}
}

func TestFetchThreadReadFailureStops(t *testing.T) {
	f := defaultFake()
	f.graphql = func(string, map[string]any) (string, int) { return `{"message":"boom"}`, 502 }
	c := newFake(t, f)
	_, err := c.Fetch(FetchOptions{PR: 7})
	var te *ThreadsError
	if !errors.As(err, &te) {
		t.Fatalf("err = %v, want a ThreadsError instead of every thread treated as open", err)
	}
}

func TestThreadsPagesCommentsInsideAThread(t *testing.T) {
	f := defaultFake()
	f.graphql = func(q string, vars map[string]any) (string, int) {
		if strings.Contains(q, "node(id:") {
			return `{"data":{"node":{"comments":{"pageInfo":{"hasNextPage":false},"nodes":[{"databaseId":999}]}}}}`, 200
		}
		return `{"data":{"repository":{"pullRequest":{"reviewThreads":{
		  "pageInfo":{"hasNextPage":false},
		  "nodes":[{"id":"TB","isResolved":true,"isOutdated":false,
		    "comments":{"totalCount":101,"pageInfo":{"hasNextPage":true,"endCursor":"C"},"nodes":[{"databaseId":1}]}}]}}}}}`, 200
	}
	c := newFake(t, f)
	threads, err := c.Threads(7)
	if err != nil {
		t.Fatal(err)
	}
	if threads[999] == nil || !threads[999].Resolved || threads[999].RootID != 1 {
		t.Fatalf("comment past the first 100 of its thread = %+v", threads[999])
	}
}

func TestSplitWritesOneFilePerComment(t *testing.T) {
	c := newFake(t, defaultFake())
	res, err := c.Fetch(FetchOptions{PR: 7})
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(t.TempDir(), "triage-7")
	out, err := Split(res, dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Comments) != 3 {
		t.Fatalf("got %d entries", len(out.Comments))
	}
	want := filepath.Join(dir, "review_comment_101.json")
	if out.Comments[0].CommentFile != want {
		t.Errorf("comment_file = %s, want %s", out.Comments[0].CommentFile, want)
	}
	b, err := os.ReadFile(want)
	if err != nil {
		t.Fatal(err)
	}
	var cm Comment
	if err := json.Unmarshal(b, &cm); err != nil || cm.Body != "a" {
		t.Errorf("file holds %s", b)
	}
	meta, _ := Encode(out)
	if strings.Contains(string(meta), `"body"`) {
		t.Error("split metadata carries the bodies")
	}
}

func ref(s string) Ref {
	r, err := ParseRef(s)
	if err != nil {
		panic(err)
	}
	return r
}

func intp(n int) *int { return &n }

func TestDismissOnPage2ThreadWithMatchingSeen(t *testing.T) {
	f := defaultFake()
	c := newFake(t, f)
	r := c.Dismiss(7, ref("review_comment:301"), intp(2), "YAGNI")
	if r.Code != ExitOK || r.Status != "dismissed" {
		t.Fatalf("report = %s (code %d)", r.JSON(), r.Code)
	}
	if !strings.Contains(r.JSON(), `"thread_id":"T3"`) {
		t.Errorf("report = %s, want thread T3", r.JSON())
	}
	if len(f.posts) != 1 || f.posts[0].Body["in_reply_to"] != float64(301) {
		t.Errorf("posts = %+v, want one reply to 301", f.posts)
	}
}

func TestDismissOnAReplyAnswersTheThreadRoot(t *testing.T) {
	f := defaultFake()
	c := newFake(t, f)
	r := c.Dismiss(7, ref("review_comment:302"), nil, "dup")
	if r.Code != ExitOK || !strings.Contains(r.JSON(), `"thread_id":"T3"`) {
		t.Fatalf("report = %s (code %d)", r.JSON(), r.Code)
	}
	if len(f.posts) != 1 || f.posts[0].Body["in_reply_to"] != float64(301) {
		t.Errorf("posts = %+v, want the reply aimed at the root 301", f.posts)
	}
}

func TestDismissSkipsAThreadThatGrew(t *testing.T) {
	f := defaultFake()
	c := newFake(t, f)
	r := c.Dismiss(7, ref("review_comment:301"), intp(1), "YAGNI")
	if r.Code != ExitChanged || r.Status != "changed" {
		t.Fatalf("report = %s (code %d)", r.JSON(), r.Code)
	}
	if len(f.posts) != 0 {
		t.Error("posted to a changed thread")
	}
}

func TestDismissSkipsAThreadResolvedMeanwhile(t *testing.T) {
	f := defaultFake()
	c := newFake(t, f)
	r := c.Dismiss(7, ref("review_comment:201"), nil, "YAGNI")
	if r.Code != ExitChanged || len(f.posts) != 0 {
		t.Fatalf("report = %s (code %d), posts %d", r.JSON(), r.Code, len(f.posts))
	}
}

func TestDismissReplyPostedButResolveFailed(t *testing.T) {
	f := defaultFake()
	f.graphql = defaultGraphQL(false)
	c := newFake(t, f)
	r := c.Dismiss(7, ref("review_comment:101"), nil, "YAGNI")
	if r.Code != ExitNotResolved || r.Status != "replied_not_resolved" || !r.Stderr {
		t.Fatalf("report = %s (code %d)", r.JSON(), r.Code)
	}
}

func TestDismissFailedReplyNeverResolves(t *testing.T) {
	f := defaultFake()
	f.postStatus = 500
	c := newFake(t, f)
	r := c.Dismiss(7, ref("review_comment:101"), nil, "YAGNI")
	if r.Code != ExitError || f.mutations != 0 {
		t.Fatalf("report = %s (code %d), mutations %d", r.JSON(), r.Code, f.mutations)
	}
}

func TestDismissIssueCommentPostsANewComment(t *testing.T) {
	f := defaultFake()
	c := newFake(t, f)
	r := c.Dismiss(7, ref("issue_comment:55"), nil, "")
	if r.Code != ExitOK || r.Status != "dismissed" {
		t.Fatalf("report = %s", r.JSON())
	}
	if len(f.posts) != 1 || f.posts[0].Path != "/repos/o/r/issues/7/comments" ||
		!strings.HasPrefix(f.posts[0].Body["body"].(string), "Re: comment #55\n\n**Dismissed**") {
		t.Errorf("posts = %+v", f.posts)
	}
}

func TestResolveAlreadyResolvedIsANoOp(t *testing.T) {
	f := defaultFake()
	c := newFake(t, f)
	r := c.Resolve(7, ref("review_comment:201"), nil)
	if r.Code != ExitOK || r.Status != "already_resolved" || f.mutations != 0 {
		t.Fatalf("report = %s, mutations %d", r.JSON(), f.mutations)
	}
}

func TestResolveOpenThread(t *testing.T) {
	f := defaultFake()
	c := newFake(t, f)
	r := c.Resolve(7, ref("review_comment:101"), intp(1))
	if r.Code != ExitOK || r.JSON() != `{"status":"resolved","ref_id":"review_comment:101","thread_id":"T1"}` {
		t.Fatalf("report = %s (code %d)", r.JSON(), r.Code)
	}
}

func TestResolveRejectsNonInlineAndUnknownComments(t *testing.T) {
	c := newFake(t, defaultFake())
	if r := c.Resolve(7, ref("issue_comment:1"), nil); r.Code != ExitError {
		t.Errorf("issue comment: %s", r.JSON())
	}
	if r := c.Resolve(7, ref("review_comment:12345"), nil); r.Code != ExitError ||
		!strings.Contains(r.JSON(), "Could not find thread") {
		t.Errorf("unknown comment: %s", r.JSON())
	}
}

func TestParseRef(t *testing.T) {
	for _, bad := range []string{"review_comment", "review_comment:abc", ""} {
		if _, err := ParseRef(bad); err == nil {
			t.Errorf("ParseRef(%q) accepted", bad)
		}
	}
}

func TestReact(t *testing.T) {
	f := defaultFake()
	c := newFake(t, f)
	if r := c.React(ref("review_comment:101")); r.Status != "reacted" ||
		f.posts[0].Path != "/repos/o/r/pulls/comments/101/reactions" {
		t.Fatalf("report = %s, posts %+v", r.JSON(), f.posts)
	}
	if r := c.React(ref("review:3")); r.Code != ExitError {
		t.Errorf("review reaction: %s", r.JSON())
	}
}

// fakeClock advances only when the watcher sleeps.
type fakeClock struct{ now time.Time }

func (k *fakeClock) Now() time.Time        { return k.now }
func (k *fakeClock) Sleep(d time.Duration) { k.now = k.now.Add(d) }
func newWatcher(c *Client, out *strings.Builder, checks ...[]Check) (*Watcher, *fakeClock) {
	k := &fakeClock{now: time.Unix(0, 0)}
	poll := 0
	return &Watcher{
		Client: c, Repo: "o/r", PR: 7,
		Timeout: 300 * time.Second, Interval: 30 * time.Second, NoChecksGrace: 60 * time.Second,
		Checks: func() []Check {
			cs := checks[min(poll, len(checks)-1)]
			poll++
			return cs
		},
		Now: k.Now, Sleep: k.Sleep, Out: out,
	}, k
}

func TestWatchReportsFailedChecksAndCopilot(t *testing.T) {
	f := defaultFake()
	f.rest["/repos/o/r/pulls/7/reviews"] = []string{
		`[{"id":1,"user":{"login":"copilot-pull-request-reviewer[bot]"},"commit_id":"old","html_url":"u-old"},
		  {"id":2,"user":{"login":"copilot-pull-request-reviewer[bot]"},"commit_id":"abcdef1234567","html_url":"u-new"}]`,
	}
	f.rest["/repos/o/r/pulls/7/reviews/2/comments"] = []string{`[{"id":1},{"id":2}]`}
	var out strings.Builder
	w, _ := newWatcher(newFake(t, f), &out,
		[]Check{{Name: "lint", Bucket: "pass"}, {Name: "test", Bucket: "pending"}},
		[]Check{{Name: "lint", Bucket: "pass"}, {Name: "test", Bucket: "fail", Link: "L"}},
	)
	w.Copilot = true
	timedOut, err := w.Run()
	if err != nil || timedOut {
		t.Fatalf("timedOut=%v err=%v", timedOut, err)
	}
	want := `Watching o/r#7 at abcdef1 (copilot: true, timeout: 300s)
CI lint: pass
Copilot reviewed: 2 inline comments
CI test: fail
CI finished: fail
---
CI: fail
FAILED: test L
COPILOT: 2 inline comments
COPILOT_REVIEW: u-new
`
	if out.String() != want {
		t.Errorf("output:\n%s\nwant:\n%s", out.String(), want)
	}
}

func TestWatchNoChecksAfterGrace(t *testing.T) {
	var out strings.Builder
	w, k := newWatcher(newFake(t, defaultFake()), &out, nil)
	timedOut, err := w.Run()
	if err != nil || timedOut {
		t.Fatalf("timedOut=%v err=%v", timedOut, err)
	}
	if !strings.Contains(out.String(), "CI: none\nCOPILOT: not-waited\n") {
		t.Errorf("output:\n%s", out.String())
	}
	if k.now.Sub(time.Unix(0, 0)) < 60*time.Second {
		t.Error("gave up before the grace period")
	}
}

func TestWatchTimesOutWithTheFinalBlock(t *testing.T) {
	var out strings.Builder
	w, _ := newWatcher(newFake(t, defaultFake()), &out, []Check{{Name: "test", Bucket: "pending"}})
	timedOut, err := w.Run()
	if err != nil || !timedOut {
		t.Fatalf("timedOut=%v err=%v", timedOut, err)
	}
	if !strings.HasSuffix(out.String(), "---\nCI: pending\nCOPILOT: not-waited\n") {
		t.Errorf("output:\n%s", out.String())
	}
}
