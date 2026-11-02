// Package models contains database row mappings only. The schema is owned by
// ../schema.sql; generating these mappings never creates or alters tables.
package models

//go:generate python3 ../../../scripts/gen-storage-models.py
