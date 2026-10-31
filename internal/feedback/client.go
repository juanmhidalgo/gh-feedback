// Package feedback reads and acts on the review feedback of a GitHub pull
// request: issue comments, inline review comments, reviews and their threads.
package feedback

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"

	"github.com/cli/go-gh/v2/pkg/api"
	"github.com/cli/go-gh/v2/pkg/repository"
)

// Client talks to one repository through the REST and GraphQL APIs.
type Client struct {
	REST  *api.RESTClient
	GQL   *api.GraphQLClient
	Owner string
	Repo  string
}

// New builds a client for repo with the auth and host settings of gh.
func New(repo repository.Repository) (*Client, error) {
	return NewWithOptions(repo, api.ClientOptions{Host: repo.Host})
}

// NewWithOptions builds a client with explicit options; tests use it to point
// the client at a fake server.
func NewWithOptions(repo repository.Repository, opts api.ClientOptions) (*Client, error) {
	rest, err := api.NewRESTClient(opts)
	if err != nil {
		return nil, err
	}
	gql, err := api.NewGraphQLClient(opts)
	if err != nil {
		return nil, err
	}
	return &Client{REST: rest, GQL: gql, Owner: repo.Owner, Repo: repo.Name}, nil
}

func (c *Client) path(format string, a ...any) string {
	return fmt.Sprintf("repos/%s/%s/", c.Owner, c.Repo) + fmt.Sprintf(format, a...)
}

var linkNext = regexp.MustCompile(`<([^>]+)>;\s*rel="next"`)

// getAll reads every page of a REST list, following the Link header.
func getAll[T any](c *Client, path string) ([]T, error) {
	all := []T{}
	for next := path; next != ""; {
		resp, err := c.REST.Request(http.MethodGet, next, nil)
		if err != nil {
			return nil, err
		}
		var page []T
		err = json.NewDecoder(resp.Body).Decode(&page)
		resp.Body.Close()
		if err != nil {
			return nil, err
		}
		all = append(all, page...)
		next = ""
		if m := linkNext.FindStringSubmatch(resp.Header.Get("Link")); m != nil {
			next = m[1]
		}
	}
	return all, nil
}

func (c *Client) post(path string, body any) error {
	b, err := json.Marshal(body)
	if err != nil {
		return err
	}
	return c.REST.Post(path, bytes.NewReader(b), nil)
}

type user struct {
	Login string `json:"login"`
	Type  string `json:"type"`
}

// A comment is a bot's when GitHub says so (`type == "Bot"`: Copilot, every
// `<app>[bot]`) or its login is a known bot account. Matching a bare "bot"
// substring would also take humans such as "abbott" or "robotlover".
var botLogin = regexp.MustCompile(`(?i)\[bot\]$|^(copilot|gemini-code-assist|coderabbitai|claude|chatgpt-codex-connector|sourcery-ai|dependabot|renovate|greenkeeper|codecov|sonarcloud|github-actions)$`)

func isBot(u user) bool {
	return u.Type == "Bot" || botLogin.MatchString(u.Login)
}

// encode writes v as JSON without escaping <, > and &: comment bodies are
// code and prose, and < helps no reader.
func encode(v any, indent bool) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if indent {
		enc.SetIndent("", "  ")
	}
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
