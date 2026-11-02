package artifact

import (
	"path"
	"regexp"
	slices "slices"
	"strings"

	domain "github.com/krun-dev/krun/internal/domain"
)

const WorkerSourceFilesMax = 512

const WorkerSourceBytesMax = 2 << 20

// Source bundles use the portable ustar name field. Keeping `src/` + path at
// 100 UTF-8 bytes avoids PAX headers, which are deliberately not accepted by
// the short-lived builder's narrow archive parser.
const WorkerSourcePathMax = 96

func SourceValid(entry string, files map[string]string) error {
	if len(files) == 0 || len(files) > WorkerSourceFilesMax {
		return domain.Problem(422, "WORKER_SOURCE_INVALID", "Provide 1–512 source files")
	}
	total := 0
	valid := regexp.MustCompile(`^[a-zA-Z0-9_./-]+$`)
	for name, code := range files {
		ext := path.Ext(name)
		if strings.HasPrefix(path.Base(name), "__krun_") || !valid.MatchString(name) || path.Clean(name) != name || strings.HasPrefix(name, "/") || strings.HasPrefix(name, "../") || len(name) > WorkerSourcePathMax || !slices.Contains([]string{".js", ".mjs", ".cjs", ".jsx", ".ts", ".tsx", ".json"}, ext) {
			return domain.Problem(422, "WORKER_SOURCE_INVALID", "Only safe relative JS, JSX, TS, TSX and JSON paths are supported")
		}
		total += len(code)
	}
	if total > WorkerSourceBytesMax {
		return domain.Problem(413, "WORKER_SOURCE_INVALID", "Source exceeds 2 MiB")
	}
	return SourceEntryValid(entry, files)
}

// sourceEntryValid checks just the entrypoint. Repository builds resolve the
// entrypoint from wrangler config after the tree is fetched, so the file-level
// checks and the entrypoint check happen at different times.
func SourceEntryValid(entry string, files map[string]string) error {
	if _, ok := files[entry]; !ok || !slices.Contains([]string{".js", ".mjs", ".cjs", ".jsx", ".ts", ".tsx"}, path.Ext(entry)) {
		return domain.Problem(422, "WORKER_SOURCE_INVALID", "Entrypoint must be an included JS, JSX, TS or TSX file")
	}
	return nil
}
