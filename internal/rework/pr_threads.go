package rework

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/iamseth/tao/internal/commandrunner"
)

// PRThreadAuthorScope controls whose pull-request threads are returned.
type PRThreadAuthorScope string

const (
	// PRThreadAuthorsOwner returns threads started by the authenticated GitHub user.
	PRThreadAuthorsOwner PRThreadAuthorScope = "owner"
	// PRThreadAuthorsAll returns threads started by any GitHub user.
	PRThreadAuthorsAll PRThreadAuthorScope = "all"
)

// PRThreadComment is one comment in a pull-request review thread.
type PRThreadComment struct {
	NodeID      string
	Body        string
	AuthorLogin string
}

// PRThread is a normalized pull-request review thread. A nil Line is retained
// for outdated comments because downstream finding normalization does not rely
// on line numbers.
type PRThread struct {
	NodeID     string
	Path       string
	Line       *int
	IsResolved bool
	IsOutdated bool
	Comments   []PRThreadComment
}

// PRThreadReadRequest identifies one GitHub pull request and the desired author scope.
type PRThreadReadRequest struct {
	RepoRoot          string
	RepositoryOwner   string
	RepositoryName    string
	PullRequestNumber int
	AuthorScope       PRThreadAuthorScope
}

// PRThreadReadResult contains the authenticated owner and normalized threads.
type PRThreadReadResult struct {
	OwnerLogin string
	Threads    []PRThread
}

// PRThreadReader reads pull-request review threads through an injectable gh runner.
type PRThreadReader struct {
	CommandRunner commandrunner.Runner
}

// Read fetches the viewer and review threads in one GraphQL document, then
// removes resolved and out-of-scope threads.
func (r PRThreadReader) Read(ctx context.Context, request PRThreadReadRequest) (PRThreadReadResult, error) {
	if strings.TrimSpace(request.RepoRoot) == "" {
		return PRThreadReadResult{}, fmt.Errorf("read pull-request threads: repo root is empty")
	}
	if strings.TrimSpace(request.RepositoryOwner) == "" {
		return PRThreadReadResult{}, fmt.Errorf("read pull-request threads: repository owner is empty")
	}
	if strings.TrimSpace(request.RepositoryName) == "" {
		return PRThreadReadResult{}, fmt.Errorf("read pull-request threads: repository name is empty")
	}
	if request.PullRequestNumber <= 0 {
		return PRThreadReadResult{}, fmt.Errorf("read pull-request threads: pull-request number must be positive")
	}
	scope := request.AuthorScope
	if scope == "" {
		scope = PRThreadAuthorsOwner
	}
	if scope != PRThreadAuthorsOwner && scope != PRThreadAuthorsAll {
		return PRThreadReadResult{}, fmt.Errorf("read pull-request threads: unsupported author scope %q", scope)
	}

	args := []string{
		"api", "graphql", "--method", "POST", "--paginate",
		"-f", "query=" + pullRequestThreadsQuery,
		"-f", "owner=" + request.RepositoryOwner,
		"-f", "name=" + request.RepositoryName,
		"-F", "number=" + strconv.Itoa(request.PullRequestNumber),
	}
	runner := r.CommandRunner
	if runner == nil {
		runner = commandrunner.DefaultLocal
	}
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	if err := runner(ctx, request.RepoRoot, "gh", args, &stdout, &stderr); err != nil {
		if detail := strings.TrimSpace(stderr.String()); detail != "" {
			return PRThreadReadResult{}, fmt.Errorf("gh api graphql: %w: %s", err, detail)
		}
		return PRThreadReadResult{}, fmt.Errorf("gh api graphql: %w", err)
	}

	return normalizePRThreadResponse(stdout.Bytes(), scope)
}

const pullRequestThreadsQuery = `query TaoPullRequestReviewThreads($owner: String!, $name: String!, $number: Int!, $endCursor: String) {
  viewer { login }
  repository(owner: $owner, name: $name) {
    pullRequest(number: $number) {
      reviewThreads(first: 100, after: $endCursor) {
        pageInfo { hasNextPage endCursor }
        nodes {
          id
          path
          line
          isResolved
          isOutdated
          comments(first: 100) {
            nodes {
              id
              body
              author { login }
              replyTo { id }
              pullRequestReview { state }
            }
          }
        }
      }
    }
  }
}`

type prThreadGraphQLResponse struct {
	Data struct {
		Viewer struct {
			Login string `json:"login"`
		} `json:"viewer"`
		Repository *struct {
			PullRequest *struct {
				ReviewThreads struct {
					Nodes []struct {
						ID         string `json:"id"`
						Path       string `json:"path"`
						Line       *int   `json:"line"`
						IsResolved bool   `json:"isResolved"`
						IsOutdated bool   `json:"isOutdated"`
						Comments   struct {
							Nodes []struct {
								ID     string `json:"id"`
								Body   string `json:"body"`
								Author *struct {
									Login string `json:"login"`
								} `json:"author"`
								ReplyTo *struct {
									ID string `json:"id"`
								} `json:"replyTo"`
								PullRequestReview *struct {
									State string `json:"state"`
								} `json:"pullRequestReview"`
							} `json:"nodes"`
						} `json:"comments"`
					} `json:"nodes"`
				} `json:"reviewThreads"`
			} `json:"pullRequest"`
		} `json:"repository"`
	} `json:"data"`
	Errors []struct {
		Message string `json:"message"`
	} `json:"errors"`
}

func normalizePRThreadResponse(data []byte, scope PRThreadAuthorScope) (PRThreadReadResult, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	ownerLogin := ""
	threads := make([]PRThread, 0)
	threadIndexes := make(map[string]int)
	commentIDs := make(map[string]map[string]struct{})
	hasSubmittedRoot := make(map[string]bool)
	pages := 0
	for {
		var response prThreadGraphQLResponse
		if err := decoder.Decode(&response); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return PRThreadReadResult{}, fmt.Errorf("parse gh pull-request threads: %w", err)
		}
		pages++
		if len(response.Errors) > 0 {
			messages := make([]string, 0, len(response.Errors))
			for _, graphqlErr := range response.Errors {
				if message := strings.TrimSpace(graphqlErr.Message); message != "" {
					messages = append(messages, message)
				}
			}
			if len(messages) == 0 {
				messages = append(messages, "unknown GraphQL error")
			}
			return PRThreadReadResult{}, fmt.Errorf("read pull-request threads: %s", strings.Join(messages, "; "))
		}
		pageOwner := strings.TrimSpace(response.Data.Viewer.Login)
		if pageOwner == "" {
			return PRThreadReadResult{}, fmt.Errorf("read pull-request threads: GraphQL response is missing viewer login")
		}
		if ownerLogin == "" {
			ownerLogin = pageOwner
		} else if !strings.EqualFold(ownerLogin, pageOwner) {
			return PRThreadReadResult{}, fmt.Errorf("read pull-request threads: GraphQL response viewer changed during pagination")
		}
		if response.Data.Repository == nil || response.Data.Repository.PullRequest == nil {
			return PRThreadReadResult{}, fmt.Errorf("read pull-request threads: GraphQL response is missing pull request")
		}

		for _, node := range response.Data.Repository.PullRequest.ReviewThreads.Nodes {
			nodeID := strings.TrimSpace(node.ID)
			if nodeID == "" {
				continue
			}
			index, exists := threadIndexes[nodeID]
			if !exists {
				index = len(threads)
				threadIndexes[nodeID] = index
				threads = append(threads, PRThread{
					NodeID:     nodeID,
					Path:       node.Path,
					Line:       cloneInt(node.Line),
					IsResolved: node.IsResolved,
					IsOutdated: node.IsOutdated,
				})
				commentIDs[nodeID] = make(map[string]struct{})
			} else {
				thread := &threads[index]
				thread.IsResolved = thread.IsResolved || node.IsResolved
				thread.IsOutdated = thread.IsOutdated || node.IsOutdated
				if thread.Path == "" {
					thread.Path = node.Path
				}
				if thread.Line == nil {
					thread.Line = cloneInt(node.Line)
				}
			}

			thread := &threads[index]
			for _, commentNode := range node.Comments.Nodes {
				reviewState := ""
				if commentNode.PullRequestReview != nil {
					reviewState = strings.TrimSpace(commentNode.PullRequestReview.State)
				}
				if reviewState == "" || strings.EqualFold(reviewState, "PENDING") {
					continue
				}
				if commentNode.ReplyTo == nil {
					hasSubmittedRoot[nodeID] = true
				}
				commentID := strings.TrimSpace(commentNode.ID)
				if commentID != "" {
					if _, duplicate := commentIDs[nodeID][commentID]; duplicate {
						continue
					}
					commentIDs[nodeID][commentID] = struct{}{}
				}
				authorLogin := ""
				if commentNode.Author != nil {
					authorLogin = strings.TrimSpace(commentNode.Author.Login)
				}
				thread.Comments = append(thread.Comments, PRThreadComment{
					NodeID:      commentID,
					Body:        commentNode.Body,
					AuthorLogin: authorLogin,
				})
			}
		}
	}
	if pages == 0 {
		return PRThreadReadResult{}, fmt.Errorf("parse gh pull-request threads: empty response")
	}

	filtered := threads[:0]
	for _, thread := range threads {
		if thread.IsResolved || !hasSubmittedRoot[thread.NodeID] {
			continue
		}
		if scope == PRThreadAuthorsOwner && (len(thread.Comments) == 0 || !strings.EqualFold(thread.Comments[0].AuthorLogin, ownerLogin)) {
			continue
		}
		filtered = append(filtered, thread)
	}
	return PRThreadReadResult{OwnerLogin: ownerLogin, Threads: filtered}, nil
}

func cloneInt(value *int) *int {
	if value == nil {
		return nil
	}
	clone := *value
	return &clone
}
