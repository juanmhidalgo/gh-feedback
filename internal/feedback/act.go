package feedback

import (
	"fmt"
	"strconv"
	"strings"
)

// Exit codes of resolve, dismiss and react.
const (
	ExitOK          = 0
	ExitError       = 1 // nothing was posted
	ExitNotResolved = 5 // the reply was posted, but the thread was not resolved
	ExitChanged     = 6 // the thread changed since triage read it; nothing was posted
)

// Ref identifies a comment as `<type>:<id>`, the ref_id printed by fetch.
type Ref struct {
	Raw  string
	Type string
	ID   int64
}

func refID(kind string, id int64) string { return kind + ":" + strconv.FormatInt(id, 10) }

func ParseRef(s string) (Ref, error) {
	kind, id, ok := strings.Cut(s, ":")
	if !ok {
		return Ref{}, fmt.Errorf("Invalid ref_id format. Expected type:id")
	}
	n, err := strconv.ParseInt(id, 10, 64)
	if err != nil {
		return Ref{}, fmt.Errorf("Invalid ref_id format. Expected type:id")
	}
	return Ref{Raw: s, Type: kind, ID: n}, nil
}

// Report is the one JSON status line an action prints: status and ref_id
// first, then Fields in order. Stderr reports go to stderr.
type Report struct {
	Status string
	RefID  string
	Fields [][2]string
	Stderr bool
	Code   int
}

func (r Report) JSON() string {
	var b strings.Builder
	b.WriteString(`{"status":` + quote(r.Status))
	if r.RefID != "" {
		b.WriteString(`,"ref_id":` + quote(r.RefID))
	}
	for _, f := range r.Fields {
		b.WriteString("," + quote(f[0]) + ":" + quote(f[1]))
	}
	b.WriteString("}")
	return b.String()
}

func quote(s string) string {
	b, _ := encode(s, false)
	return strings.TrimSuffix(string(b), "\n")
}

// ErrorReport is an error before any ref_id was understood.
func ErrorReport(msg string) Report {
	return Report{Status: "error", Fields: [][2]string{{"error", msg}}, Stderr: true, Code: ExitError}
}

func errReport(ref Ref, code int, fields ...[2]string) Report {
	return Report{Status: "error", RefID: ref.Raw, Fields: fields, Stderr: true, Code: code}
}

// loadThread finds the thread of a review comment and stops with ExitChanged
// when its comment count differs from seen (when seen is set).
func (c *Client) loadThread(pr int, ref Ref, seen *int) (*Thread, *Report) {
	threads, err := c.Threads(pr)
	if err != nil {
		r := errReport(ref, ExitError, [2]string{"error", "Could not read review threads"})
		return nil, &r
	}
	t, ok := threads[ref.ID]
	if !ok {
		r := errReport(ref, ExitError, [2]string{"error", "Could not find thread for comment"})
		return nil, &r
	}
	if seen != nil && t.Count != *seen {
		r := Report{Status: "changed", RefID: ref.Raw, Code: ExitChanged, Fields: [][2]string{
			{"thread_id", t.ID},
			{"reason", fmt.Sprintf("thread has %d comments, triage saw %d", t.Count, *seen)},
		}}
		return nil, &r
	}
	return t, nil
}

const resolveMutation = `
mutation($threadId: ID!) {
  resolveReviewThread(input: {threadId: $threadId}) {
    thread { isResolved }
  }
}`

func (c *Client) resolveThread(id string) bool {
	var resp struct {
		ResolveReviewThread struct {
			Thread struct {
				IsResolved bool `json:"isResolved"`
			} `json:"thread"`
		} `json:"resolveReviewThread"`
	}
	if err := c.GQL.Do(resolveMutation, map[string]any{"threadId": id}, &resp); err != nil {
		return false
	}
	return resp.ResolveReviewThread.Thread.IsResolved
}

// Resolve marks the thread of a review comment as resolved.
func (c *Client) Resolve(pr int, ref Ref, seen *int) Report {
	if ref.Type != "review_comment" {
		return errReport(ref, ExitError, [2]string{"error", "Only review_comment type can be resolved"})
	}
	t, stop := c.loadThread(pr, ref, seen)
	if stop != nil {
		return *stop
	}
	if t.Resolved {
		return Report{Status: "already_resolved", RefID: ref.Raw, Fields: [][2]string{{"thread_id", t.ID}}}
	}
	if !c.resolveThread(t.ID) {
		return errReport(ref, ExitError, [2]string{"thread_id", t.ID}, [2]string{"error", "Failed to resolve thread"})
	}
	return Report{Status: "resolved", RefID: ref.Raw, Fields: [][2]string{{"thread_id", t.ID}}}
}

// Dismiss replies with the reason and, for a review comment, resolves its
// thread. The thread is checked before posting: a reply cannot be taken back.
func (c *Client) Dismiss(pr int, ref Ref, seen *int, reason string) Report {
	body := "**Dismissed** - This feedback has been reviewed and dismissed."
	if reason != "" {
		body += "\n\n**Reason:** " + reason
	}

	if ref.Type != "review_comment" {
		// Issue comments and reviews have no thread: answer with a new comment.
		err := c.post(c.path("issues/%d/comments", pr), map[string]any{
			"body": fmt.Sprintf("Re: comment #%d\n\n%s", ref.ID, body),
		})
		if err != nil {
			return errReport(ref, ExitError, [2]string{"error", "Failed to post the reply; nothing was posted"})
		}
		return Report{Status: "dismissed", RefID: ref.Raw, Fields: [][2]string{{"reason", reason}}}
	}

	t, stop := c.loadThread(pr, ref, seen)
	if stop != nil {
		return *stop
	}
	if t.Resolved {
		return Report{Status: "changed", RefID: ref.Raw, Code: ExitChanged, Fields: [][2]string{
			{"thread_id", t.ID}, {"reason", "thread was resolved since triage"},
		}}
	}
	err := c.post(c.path("pulls/%d/comments", pr), map[string]any{"body": body, "in_reply_to": t.RootID})
	if err != nil {
		return errReport(ref, ExitError, [2]string{"thread_id", t.ID},
			[2]string{"error", "Failed to post the reply; nothing was posted"})
	}
	if !c.resolveThread(t.ID) {
		return Report{Status: "replied_not_resolved", RefID: ref.Raw, Stderr: true, Code: ExitNotResolved,
			Fields: [][2]string{{"thread_id", t.ID}, {"reason", reason},
				{"error", "Reply posted, but resolving the thread failed"}}}
	}
	return Report{Status: "dismissed", RefID: ref.Raw, Fields: [][2]string{{"thread_id", t.ID}, {"reason", reason}}}
}

// React adds an eyes reaction to acknowledge a comment.
func (c *Client) React(ref Ref) Report {
	var path string
	switch ref.Type {
	case "review_comment":
		path = c.path("pulls/comments/%d/reactions", ref.ID)
	case "issue_comment":
		path = c.path("issues/comments/%d/reactions", ref.ID)
	default:
		return errReport(ref, ExitError, [2]string{"error", "Cannot react to " + ref.Type + " type comments"})
	}
	if err := c.post(path, map[string]any{"content": "eyes"}); err != nil {
		return errReport(ref, ExitError, [2]string{"error", "Failed to add the reaction: " + err.Error()})
	}
	return Report{Status: "reacted", RefID: ref.Raw, Fields: [][2]string{{"reaction", "eyes"}}}
}
