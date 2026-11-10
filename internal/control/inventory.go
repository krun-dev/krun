package control

import (
	"net/http"
	"slices"

	"github.com/krun-dev/krun/internal/storage"
)

func (s *Server) systemInventory(st *State, p Principal, r *http.Request) (any, int, error) {
	if err := s.require(st, p, r, "system.inventory.read", systemTarget()); err != nil {
		return nil, 0, err
	}
	query, err := workerListOptions(r)
	if err != nil {
		return nil, 0, err
	}
	ctx := r.Context()
	switch r.PathValue("kind") {
	case "workers":
		rows, err := s.Store.ListWorkerInventory(ctx, query.after, query.search, query.limit+1)
		return inventoryPage(rows, query.limit, func(row storage.InventoryResource) string { return row.ID }, err)
	case "buckets":
		rows, err := s.Store.ListBucketInventory(ctx, query.after, query.search, query.limit+1)
		return inventoryPage(rows, query.limit, func(row storage.InventoryResource) string { return row.ID }, err)
	case "api-keys":
		status := r.URL.Query().Get("status")
		if status == "" {
			status = "all"
		}
		if !slices.Contains([]string{"all", "active", "expired", "revoked"}, status) {
			return nil, 0, problem(422, "INVALID_REQUEST", "Unknown API key status")
		}
		rows, err := s.Store.ListKeyInventory(ctx, query.after, query.search, status, query.limit+1)
		return inventoryPage(rows, query.limit, func(row storage.InventoryKey) string { return row.ID }, err)
	default:
		return nil, 0, notFound()
	}
}

func inventoryPage[T any](rows []T, limit int, id func(T) string, err error) (any, int, error) {
	if err != nil {
		return nil, 0, err
	}
	if rows == nil {
		rows = []T{}
	}
	if len(rows) > limit {
		return map[string]any{"items": rows[:limit], "next_after": id(rows[limit-1])}, http.StatusOK, nil
	}
	return map[string]any{"items": rows}, http.StatusOK, nil
}
