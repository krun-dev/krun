package control

import (
	"net/http"
	"sort"
	"strconv"
	"strings"

	workerapp "github.com/krun-dev/krun/internal/workers"
)

type workerListQuery struct {
	limit  int
	after  string
	search string
}

func workerListOptions(r *http.Request) (workerListQuery, error) {
	query := workerListQuery{
		limit:  100,
		after:  r.URL.Query().Get("after"),
		search: strings.TrimSpace(r.URL.Query().Get("q")),
	}
	if raw := r.URL.Query().Get("limit"); raw != "" {
		limit, err := strconv.Atoi(raw)
		if err != nil || limit < 1 || limit > 200 {
			return query, problem(422, "PAGE_INVALID", "limit must be between 1 and 200")
		}
		query.limit = limit
	}
	return query, nil
}

func (query workerListQuery) matches(id, name string) bool {
	search := strings.ToLower(query.search)
	return id > query.after && (search == "" ||
		strings.Contains(strings.ToLower(id), search) ||
		strings.Contains(strings.ToLower(name), search))
}

// The cursor follows the storage candidate order, including candidates hidden
// by environment permissions, so a partially visible page can still advance.
func workerPage(st *State, r *http.Request, summaries []workerapp.WorkerSummary) (any, int, error) {
	query, err := workerListOptions(r)
	if err != nil {
		return nil, 0, err
	}
	workerID := r.URL.Query().Get("worker_id")
	ids := []string{}
	for _, resource := range st.Resources {
		if resource.Kind != "worker" || resource.Organization != r.PathValue("org") {
			continue
		}
		if workerID != "" {
			if resource.ID == workerID {
				ids = append(ids, resource.ID)
			}
		} else if query.matches(resource.ID, resource.Name) {
			ids = append(ids, resource.ID)
		}
	}
	sort.Strings(ids)
	next := ""
	if len(ids) > query.limit {
		ids = ids[:query.limit]
		next = ids[len(ids)-1]
	}
	included := make(map[string]bool, len(ids))
	for _, id := range ids {
		included[id] = true
	}
	items := []workerapp.WorkerSummary{}
	for _, summary := range summaries {
		if included[summary.ID] {
			items = append(items, summary)
		}
	}
	out := map[string]any{"items": items}
	if next != "" {
		out["next_after"] = next
	}
	return out, http.StatusOK, nil
}
