package feedback

import (
	"os"
	"path/filepath"
	"strings"
)

// Comment is one piece of feedback. Inline review comments carry the file,
// line and thread fields; reviews carry the state.
type Comment struct {
	RefID          string  `json:"ref_id"`
	Type           string  `json:"type"`
	Author         string  `json:"author"`
	IsBot          bool    `json:"is_bot"`
	Body           string  `json:"body"`
	File           *string `json:"file,omitempty"`
	Line           *int    `json:"line,omitempty"`
	Resolved       *bool   `json:"resolved,omitempty"`
	Outdated       *bool   `json:"outdated,omitempty"`
	ThreadID       *string `json:"thread_id,omitempty"`
	ThreadComments *int    `json:"thread_comments,omitempty"`
	State          string  `json:"state,omitempty"`
}

type PRInfo struct {
	Number int    `json:"number"`
	Title  string `json:"title"`
	Author string `json:"author"`
}

type Stats struct {
	Total       int `json:"total"`
	FilteredOut struct {
		Resolved int `json:"resolved"`
		// Outdated counts the outdated comments; they are marked, not removed.
		Outdated int `json:"outdated"`
	} `json:"filtered_out"`
	Bots   int `json:"bots"`
	Humans int `json:"humans"`
	ByType struct {
		Issue  int `json:"issue"`
		Inline int `json:"inline"`
		Review int `json:"review"`
	} `json:"by_type"`
}

type FetchResult struct {
	PR       PRInfo    `json:"pr"`
	Stats    Stats     `json:"stats"`
	Comments []Comment `json:"comments"`
}

type FetchOptions struct {
	PR int
	// MaxBody truncates bodies to that many characters; 0 keeps them whole.
	MaxBody         int
	BotsOnly        bool
	IncludeResolved bool
}

// Fetch reads every comment, inline comment and review of a PR, every page of
// each, and marks each inline comment with its thread's state.
func (c *Client) Fetch(opts FetchOptions) (*FetchResult, error) {
	threads, err := c.Threads(opts.PR)
	if err != nil {
		return nil, err
	}

	var pr struct {
		Number int    `json:"number"`
		Title  string `json:"title"`
		User   user   `json:"user"`
	}
	if err := c.REST.Get(c.path("pulls/%d", opts.PR), &pr); err != nil {
		return nil, err
	}

	issueComments, err := getAll[struct {
		ID   int64  `json:"id"`
		User user   `json:"user"`
		Body string `json:"body"`
	}](c, c.path("issues/%d/comments?per_page=100", opts.PR))
	if err != nil {
		return nil, err
	}
	reviewComments, err := getAll[struct {
		ID           int64  `json:"id"`
		User         user   `json:"user"`
		Body         string `json:"body"`
		Path         string `json:"path"`
		Line         *int   `json:"line"`
		OriginalLine *int   `json:"original_line"`
	}](c, c.path("pulls/%d/comments?per_page=100", opts.PR))
	if err != nil {
		return nil, err
	}
	reviews, err := getAll[struct {
		ID    int64  `json:"id"`
		User  user   `json:"user"`
		Body  string `json:"body"`
		State string `json:"state"`
	}](c, c.path("pulls/%d/reviews?per_page=100", opts.PR))
	if err != nil {
		return nil, err
	}

	all := []Comment{}
	for _, ic := range issueComments {
		all = append(all, Comment{
			RefID: refID("issue_comment", ic.ID), Type: "issue_comment",
			Author: ic.User.Login, IsBot: isBot(ic.User), Body: truncate(ic.Body, opts.MaxBody),
		})
	}
	for _, rc := range reviewComments {
		line := rc.Line
		if line == nil {
			line = rc.OriginalLine
		}
		path := rc.Path
		cm := Comment{
			RefID: refID("review_comment", rc.ID), Type: "review_comment",
			Author: rc.User.Login, IsBot: isBot(rc.User), Body: truncate(rc.Body, opts.MaxBody),
			File: &path, Line: line,
		}
		resolved, outdated := false, false
		if t, ok := threads[rc.ID]; ok {
			resolved, outdated = t.Resolved, t.Outdated
			id, count := t.ID, t.Count
			cm.ThreadID, cm.ThreadComments = &id, &count
		}
		cm.Resolved, cm.Outdated = &resolved, &outdated
		all = append(all, cm)
	}
	for _, rv := range reviews {
		if rv.Body == "" && rv.State != "APPROVED" && rv.State != "CHANGES_REQUESTED" {
			continue
		}
		all = append(all, Comment{
			RefID: refID("review", rv.ID), Type: "review",
			Author: rv.User.Login, IsBot: isBot(rv.User), Body: truncate(rv.Body, opts.MaxBody),
			State: rv.State,
		})
	}

	res := &FetchResult{
		PR:       PRInfo{Number: pr.Number, Title: pr.Title, Author: pr.User.Login},
		Comments: []Comment{},
	}
	for _, cm := range all {
		if opts.BotsOnly && !cm.IsBot {
			continue
		}
		// Resolved and outdated are counted after the bot filter and before
		// the resolved filter, so the stats say what was left out.
		isResolved := cm.Resolved != nil && *cm.Resolved
		if isResolved {
			res.Stats.FilteredOut.Resolved++
		}
		if cm.Outdated != nil && *cm.Outdated {
			res.Stats.FilteredOut.Outdated++
		}
		if isResolved && !opts.IncludeResolved {
			continue
		}
		res.Comments = append(res.Comments, cm)
		if cm.IsBot {
			res.Stats.Bots++
		} else {
			res.Stats.Humans++
		}
		switch cm.Type {
		case "issue_comment":
			res.Stats.ByType.Issue++
		case "review_comment":
			res.Stats.ByType.Inline++
		case "review":
			res.Stats.ByType.Review++
		}
	}
	res.Stats.Total = len(res.Comments)
	return res, nil
}

// SplitComment is a comment's metadata without its body. Absent fields are
// null, not missing, so every entry has the same keys.
type SplitComment struct {
	RefID          string  `json:"ref_id"`
	Type           string  `json:"type"`
	Author         string  `json:"author"`
	IsBot          bool    `json:"is_bot"`
	File           *string `json:"file"`
	Line           *int    `json:"line"`
	ThreadComments *int    `json:"thread_comments"`
	CommentFile    string  `json:"comment_file"`
}

type SplitResult struct {
	PR       PRInfo         `json:"pr"`
	Stats    Stats          `json:"stats"`
	Comments []SplitComment `json:"comments"`
}

// Split writes each comment to <dir>/<ref_id>.json (":" becomes "_") and
// returns the result without the bodies. The bodies are untrusted text: they
// reach whoever evaluates them through the files and never pass through the
// caller.
func Split(res *FetchResult, dir string) (*SplitResult, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	out := &SplitResult{PR: res.PR, Stats: res.Stats, Comments: []SplitComment{}}
	for _, cm := range res.Comments {
		file := filepath.Join(dir, strings.ReplaceAll(cm.RefID, ":", "_")+".json")
		b, err := encode(cm, false)
		if err != nil {
			return nil, err
		}
		if err := os.WriteFile(file, b, 0o644); err != nil {
			return nil, err
		}
		out.Comments = append(out.Comments, SplitComment{
			RefID: cm.RefID, Type: cm.Type, Author: cm.Author, IsBot: cm.IsBot,
			File: cm.File, Line: cm.Line, ThreadComments: cm.ThreadComments, CommentFile: file,
		})
	}
	return out, nil
}

// Encode renders a result as indented JSON.
func Encode(v any) ([]byte, error) { return encode(v, true) }

func truncate(s string, max int) string {
	if max <= 0 {
		return s
	}
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max]) + "... [truncated]"
}
