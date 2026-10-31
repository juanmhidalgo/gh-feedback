package feedback

// Thread is a review thread as GraphQL reports it.
type Thread struct {
	ID       string
	Resolved bool
	Outdated bool
	// Count is the thread's comment count; `--seen` compares against it.
	Count int
	// RootID is the databaseId of the thread's first comment, the one a reply
	// has to point at: the API does not take replies to replies.
	RootID int64
}

// ThreadsError means the review threads could not be read. Callers stop on it
// instead of treating every thread as open.
type ThreadsError struct{ Err error }

func (e *ThreadsError) Error() string { return "could not read review threads: " + e.Err.Error() }
func (e *ThreadsError) Unwrap() error { return e.Err }

const threadsQuery = `
query($owner: String!, $repo: String!, $pr: Int!, $after: String) {
  repository(owner: $owner, name: $repo) {
    pullRequest(number: $pr) {
      reviewThreads(first: 100, after: $after) {
        pageInfo { hasNextPage endCursor }
        nodes {
          id
          isResolved
          isOutdated
          comments(first: 100) {
            totalCount
            pageInfo { hasNextPage endCursor }
            nodes { databaseId }
          }
        }
      }
    }
  }
}`

const threadCommentsQuery = `
query($id: ID!, $after: String) {
  node(id: $id) {
    ... on PullRequestReviewThread {
      comments(first: 100, after: $after) {
        pageInfo { hasNextPage endCursor }
        nodes { databaseId }
      }
    }
  }
}`

type pageInfo struct {
	HasNextPage bool   `json:"hasNextPage"`
	EndCursor   string `json:"endCursor"`
}

type commentPage struct {
	TotalCount int      `json:"totalCount"`
	PageInfo   pageInfo `json:"pageInfo"`
	Nodes      []struct {
		DatabaseID int64 `json:"databaseId"`
	} `json:"nodes"`
}

// Threads maps every comment of every review thread, the first one and each
// reply, to its thread. Both the threads and the comments inside a thread are
// paged: a PR past 100 threads, or a thread past 100 comments, would otherwise
// lose its resolution status silently.
func (c *Client) Threads(pr int) (map[int64]*Thread, error) {
	threads := map[int64]*Thread{}
	var after *string
	for {
		var resp struct {
			Repository struct {
				PullRequest struct {
					ReviewThreads struct {
						PageInfo pageInfo `json:"pageInfo"`
						Nodes    []struct {
							ID         string      `json:"id"`
							IsResolved bool        `json:"isResolved"`
							IsOutdated bool        `json:"isOutdated"`
							Comments   commentPage `json:"comments"`
						} `json:"nodes"`
					} `json:"reviewThreads"`
				} `json:"pullRequest"`
			} `json:"repository"`
		}
		vars := map[string]any{"owner": c.Owner, "repo": c.Repo, "pr": pr, "after": after}
		if err := c.GQL.Do(threadsQuery, vars, &resp); err != nil {
			return nil, &ThreadsError{err}
		}
		page := resp.Repository.PullRequest.ReviewThreads
		for _, n := range page.Nodes {
			t := &Thread{ID: n.ID, Resolved: n.IsResolved, Outdated: n.IsOutdated, Count: n.Comments.TotalCount}
			ids, err := c.threadComments(n.ID, n.Comments)
			if err != nil {
				return nil, &ThreadsError{err}
			}
			if len(ids) > 0 {
				t.RootID = ids[0]
			}
			for _, id := range ids {
				threads[id] = t
			}
		}
		if !page.PageInfo.HasNextPage {
			return threads, nil
		}
		cursor := page.PageInfo.EndCursor
		after = &cursor
	}
}

// threadComments returns the ids of every comment in a thread, reading the
// pages past the first one that came with the thread.
func (c *Client) threadComments(threadID string, first commentPage) ([]int64, error) {
	var ids []int64
	page := first
	for {
		for _, n := range page.Nodes {
			ids = append(ids, n.DatabaseID)
		}
		if !page.PageInfo.HasNextPage {
			return ids, nil
		}
		var resp struct {
			Node struct {
				Comments commentPage `json:"comments"`
			} `json:"node"`
		}
		vars := map[string]any{"id": threadID, "after": page.PageInfo.EndCursor}
		if err := c.GQL.Do(threadCommentsQuery, vars, &resp); err != nil {
			return nil, err
		}
		page = resp.Node.Comments
	}
}
