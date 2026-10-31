package feedback

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/cli/go-gh/v2/pkg/api"
	"github.com/cli/go-gh/v2/pkg/repository"
)

// fakeGitHub answers REST and GraphQL from fixtures and records every write,
// so a test can assert both on the result and on what would reach GitHub.
type fakeGitHub struct {
	mu sync.Mutex
	// rest maps a request path (without query) to its pages; page N past the
	// first is asked for with ?page=N through the Link header.
	rest map[string][]string
	// graphql answers a query given its text and variables.
	graphql func(query string, vars map[string]any) (string, int)
	// postStatus is the status for REST POSTs (default 201).
	postStatus int
	posts      []post
	mutations  int
}

type post struct {
	Path string
	Body map[string]any
}

func (f *fakeGitHub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	body, _ := io.ReadAll(r.Body)

	if r.URL.Path == "/graphql" {
		var req struct {
			Query     string         `json:"query"`
			Variables map[string]any `json:"variables"`
		}
		json.Unmarshal(body, &req)
		if strings.Contains(req.Query, "resolveReviewThread") {
			f.mutations++
		}
		out, status := f.graphql(req.Query, req.Variables)
		w.WriteHeader(status)
		io.WriteString(w, out)
		return
	}

	if r.Method == http.MethodPost {
		var b map[string]any
		json.Unmarshal(body, &b)
		f.posts = append(f.posts, post{Path: r.URL.Path, Body: b})
		status := f.postStatus
		if status == 0 {
			status = http.StatusCreated
		}
		w.WriteHeader(status)
		io.WriteString(w, "{}")
		return
	}

	pages, ok := f.rest[r.URL.Path]
	if !ok {
		w.WriteHeader(http.StatusNotFound)
		io.WriteString(w, `{"message":"Not Found"}`)
		return
	}
	n := 1
	if p := r.URL.Query().Get("page"); p != "" {
		n = int(p[0] - '0')
	}
	if n < len(pages) {
		next := *r.URL
		q := next.Query()
		q.Set("page", string(rune('0'+n+1)))
		next.RawQuery = q.Encode()
		w.Header().Set("Link", `<https://api.github.com`+next.RequestURI()+`>; rel="next"`)
	}
	io.WriteString(w, pages[n-1])
}

// redirect sends every request to the fake server, whatever its host.
type redirect struct{ target *url.URL }

func (r redirect) RoundTrip(req *http.Request) (*http.Response, error) {
	req = req.Clone(req.Context())
	req.URL.Scheme, req.URL.Host = r.target.Scheme, r.target.Host
	return http.DefaultTransport.RoundTrip(req)
}

func newFake(t *testing.T, f *fakeGitHub) *Client {
	t.Helper()
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	u, _ := url.Parse(srv.URL)
	c, err := NewWithOptions(
		repository.Repository{Host: "github.com", Owner: "o", Name: "r"},
		api.ClientOptions{Host: "github.com", AuthToken: "test", Transport: redirect{u}},
	)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// Page 1: thread T1 (comment 101, open, 1 comment). Page 2: thread T2
// (comment 201 plus reply 202, resolved) and T3 (comment 301, open, outdated,
// 2 comments).
const threadsPage1 = `{"data":{"repository":{"pullRequest":{"reviewThreads":{
  "pageInfo":{"hasNextPage":true,"endCursor":"CUR1"},
  "nodes":[{"id":"T1","isResolved":false,"isOutdated":false,
            "comments":{"totalCount":1,"pageInfo":{"hasNextPage":false},"nodes":[{"databaseId":101}]}}]}}}}}`

const threadsPage2 = `{"data":{"repository":{"pullRequest":{"reviewThreads":{
  "pageInfo":{"hasNextPage":false,"endCursor":null},
  "nodes":[{"id":"T2","isResolved":true,"isOutdated":false,
            "comments":{"totalCount":2,"pageInfo":{"hasNextPage":false},"nodes":[{"databaseId":201},{"databaseId":202}]}},
           {"id":"T3","isResolved":false,"isOutdated":true,
            "comments":{"totalCount":2,"pageInfo":{"hasNextPage":false},"nodes":[{"databaseId":301},{"databaseId":302}]}}]}}}}}`

func defaultGraphQL(resolveOK bool) func(string, map[string]any) (string, int) {
	return func(q string, vars map[string]any) (string, int) {
		switch {
		case strings.Contains(q, "resolveReviewThread"):
			if !resolveOK {
				return `{"data":{"resolveReviewThread":{"thread":{"isResolved":false}}}}`, 200
			}
			return `{"data":{"resolveReviewThread":{"thread":{"isResolved":true}}}}`, 200
		case vars["after"] == "CUR1":
			return threadsPage2, 200
		default:
			return threadsPage1, 200
		}
	}
}

func defaultFake() *fakeGitHub {
	return &fakeGitHub{
		rest: map[string][]string{
			"/repos/o/r/pulls/7": {`{"number":7,"title":"t","user":{"login":"owner"},"head":{"sha":"abcdef1234567"}}`},
			// Two REST pages, to prove nothing past the first is lost.
			"/repos/o/r/pulls/7/comments": {
				`[{"id":101,"user":{"login":"Copilot","type":"Bot"},"body":"a","path":"x.py","line":1}]`,
				`[{"id":201,"user":{"login":"abbott","type":"User"},"body":"b","path":"x.py","line":2},
				  {"id":202,"user":{"login":"owner","type":"User"},"body":"c","path":"x.py","line":2},
				  {"id":301,"user":{"login":"gemini-code-assist[bot]","type":"Bot"},"body":"d","path":"y.py","line":null,"original_line":9},
				  {"id":302,"user":{"login":"owner","type":"User"},"body":"e","path":"y.py","line":null,"original_line":9}]`,
			},
			"/repos/o/r/issues/7/comments": {`[]`},
			"/repos/o/r/pulls/7/reviews":   {`[]`},
		},
		graphql: defaultGraphQL(true),
	}
}
