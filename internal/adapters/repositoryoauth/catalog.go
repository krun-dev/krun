package repositoryoauth

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	appconfig "github.com/krun-dev/krun/internal/config"
	"golang.org/x/oauth2"
)

var (
	ErrCatalogUnauthorized = errors.New("repository authorization rejected")
	ErrCatalogForbidden    = errors.New("repository list permission denied")
	ErrCatalogUnavailable  = errors.New("repository list unavailable")
)

type Repository struct {
	Name    string `json:"name"`
	Private bool   `json:"private"`
}

const catalogPageSize = 30

// Repositories reads one bounded page from a fixed provider endpoint. Never
// follow upstream pagination URLs or return an upstream response to the caller.
func (c *Client) Repositories(ctx context.Context, provider appconfig.WorkerRepositoryProvider, token string, page int) ([]Repository, bool, error) {
	query := url.Values{"page": {strconv.Itoa(page)}, "per_page": {strconv.Itoa(catalogPageSize)}}
	base := provider.BaseURL
	path := "/api/v4/projects"
	query.Set("membership", "true")
	query.Set("min_access_level", "20")
	query.Set("order_by", "id")
	query.Set("sort", "asc")
	if provider.Type == "github" {
		base = "https://api.github.com"
		path = "/user/repos"
		query = url.Values{"page": {strconv.Itoa(page)}, "per_page": {strconv.Itoa(catalogPageSize)}, "sort": {"full_name"}}
	}

	confined, cancel, err := c.repositoryHTTPContext(ctx, provider, base, 1<<20)
	if err != nil {
		return nil, false, ErrCatalogUnavailable
	}
	defer cancel()

	req, err := http.NewRequestWithContext(confined, http.MethodGet, base+path+"?"+query.Encode(), nil)
	if err != nil {
		return nil, false, ErrCatalogUnavailable
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")
	resp, err := confined.Value(oauth2.HTTPClient).(*http.Client).Do(req)
	if err != nil {
		return nil, false, ErrCatalogUnavailable
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusUnauthorized:
		return nil, false, ErrCatalogUnauthorized
	case http.StatusForbidden:
		if resp.Header.Get("X-RateLimit-Remaining") == "0" || resp.Header.Get("Retry-After") != "" {
			return nil, false, ErrCatalogUnavailable
		}
		return nil, false, ErrCatalogForbidden
	default:
		return nil, false, ErrCatalogUnavailable
	}

	var rows []struct {
		FullName          string `json:"full_name"`
		PathWithNamespace string `json:"path_with_namespace"`
		Private           bool   `json:"private"`
		Visibility        string `json:"visibility"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&rows); err != nil || len(rows) > catalogPageSize {
		return nil, false, ErrCatalogUnavailable
	}
	out := make([]Repository, 0, len(rows))
	for _, row := range rows {
		repo := Repository{Name: row.FullName, Private: row.Private}
		if provider.Type == "gitlab" {
			repo = Repository{Name: row.PathWithNamespace, Private: row.Visibility != "public"}
		}
		if appconfig.WorkerRepositoryNameValid(repo.Name, provider.Type) {
			out = append(out, repo)
		}
	}
	more := strings.Contains(resp.Header.Get("Link"), `rel="next"`) || resp.Header.Get("X-Next-Page") != ""
	return out, more || len(rows) == catalogPageSize, nil
}
