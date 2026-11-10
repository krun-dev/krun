package artifact

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"fmt"
	"io"
	"path"
	"strings"
	"unicode/utf8"
)

// Repository builds pass a complete, already-configured Cloudflare Workers
// project to the isolated builder. This budget is deliberately separate from
// the smaller inline-source API: framework configuration, lockfiles and
// additional Text/Data/Wasm modules are build inputs even when esbuild does not
// follow them from the JavaScript entrypoint.
const WorkerRepositoryProjectFilesMax = 4096

const WorkerRepositoryProjectBytesMax = 24 << 20

const WorkerRepositoryProjectFileMax = 8 << 20

const WorkerRepositoryProjectPathMax = 240

func SafeWorkerRepositoryProjectPath(name string) bool {
	if name == "" || len(name) > WorkerRepositoryProjectPathMax || len(path.Base(name)) > 100 || path.IsAbs(name) || path.Clean(name) != name || strings.HasPrefix(name, "../") || strings.Contains(name, "\\") {
		return false
	}
	// Source bundles use portable ustar paths and the builder rejects PAX/GNU
	// extensions. Printable ASCII covers normal package and framework layouts
	// without making archive interpretation platform-dependent.
	for _, b := range []byte(name) {
		if b < 0x21 || b > 0x7e {
			return false
		}
	}
	for _, part := range strings.Split(name, "/") {
		if part == "" || part == "." || part == ".." {
			return false
		}
	}
	return true
}

// Repository credentials and local development secrets never belong in the
// untrusted build container. Package-manager registry configuration is supplied
// by the platform. Generated/cache trees are rebuilt inside the Job.
func RepositoryProjectFilePolicy(name string) (include bool, sensitive bool) {
	for _, part := range strings.Split(name, "/") {
		switch part {
		case ".git", ".wrangler", "node_modules":
			return false, false
		}
	}
	base := strings.ToLower(path.Base(name))
	switch base {
	case ".dev.vars", ".npmrc", ".yarnrc":
		return false, true
	case ".pnp.cjs", ".pnp.loader.mjs":
		return false, false
	}
	if base == ".env" || (strings.HasPrefix(base, ".env.") && !strings.HasSuffix(base, ".example")) {
		return false, true
	}
	return true, false
}

// ExtractRepositorySource retains the complete project directory needed by
// Wrangler, including assets, lockfiles and additional modules.
func ExtractRepositorySource(raw []byte, rootDirectory string) (map[string]string, error) {
	compressed, err := gzip.NewReader(bytes.NewReader(raw))
	if err != nil {
		return nil, fmt.Errorf("Repository archive is not a valid tar.gz")
	}
	defer compressed.Close()
	limited := &io.LimitedReader{R: compressed, N: RepositoryExpandedLimit + 1}
	archive := tar.NewReader(limited)
	files := make(map[string]string)
	prefix := ""
	total := 0
	for entries := 0; ; entries++ {
		header, err := archive.Next()
		if err == io.EOF {
			break
		}
		if err != nil || entries >= 10000 || limited.N <= 0 {
			return nil, fmt.Errorf("Repository archive is invalid or exceeds extraction limits")
		}
		// GitHub puts commit metadata in a global PAX header before the repository root.
		if header.Typeflag == tar.TypeXGlobalHeader || header.Typeflag == tar.TypeXHeader {
			continue
		}
		name := strings.TrimSuffix(header.Name, "/")
		if name == "" || path.Clean(name) != name || strings.HasPrefix(name, "/") || strings.HasPrefix(name, "../") {
			return nil, fmt.Errorf("Repository archive contains unsafe paths")
		}
		parts := strings.SplitN(name, "/", 2)
		if prefix == "" {
			prefix = parts[0]
		}
		if parts[0] != prefix {
			return nil, fmt.Errorf("Repository archive has multiple roots")
		}
		if len(parts) != 2 || header.Typeflag == tar.TypeDir {
			continue
		}
		name = parts[1]
		if rootDirectory != "" && rootDirectory != "." {
			if !strings.HasPrefix(name, rootDirectory+"/") {
				continue
			}
			name = strings.TrimPrefix(name, rootDirectory+"/")
		}
		if header.Typeflag == tar.TypeSymlink || header.Typeflag == tar.TypeLink {
			return nil, fmt.Errorf("Repository project directory cannot contain symbolic links or submodules")
		}
		if header.Typeflag != tar.TypeReg {
			continue
		}
		include, sensitive := RepositoryProjectFilePolicy(name)
		if sensitive {
			return nil, fmt.Errorf("Repository project contains a local credential or environment file: %s", name)
		}
		if !include {
			continue
		}
		if !SafeWorkerRepositoryProjectPath(name) || header.Size < 0 || header.Size > WorkerRepositoryProjectFileMax || header.Size > int64(WorkerRepositoryProjectBytesMax-total) || len(files) >= WorkerRepositoryProjectFilesMax {
			return nil, fmt.Errorf("Repository project must contain at most %d safe files and %d MiB; select a smaller project directory", WorkerRepositoryProjectFilesMax, WorkerRepositoryProjectBytesMax>>20)
		}
		if _, duplicate := files[name]; duplicate {
			return nil, fmt.Errorf("Repository archive contains duplicate source paths")
		}
		data, err := io.ReadAll(archive)
		if err != nil || len(data) != int(header.Size) {
			return nil, fmt.Errorf("Repository project file is invalid: %s", name)
		}
		switch strings.ToLower(path.Ext(name)) {
		case ".js", ".mjs", ".cjs", ".jsx", ".ts", ".tsx", ".json", ".jsonc", ".toml":
			if !utf8.Valid(data) {
				return nil, fmt.Errorf("Repository project text file is not valid UTF-8: %s", name)
			}
		}
		if strings.EqualFold(path.Base(name), ".yarnrc.yml") {
			config := strings.ToLower(string(data))
			if strings.Contains(config, "npmauthtoken") || strings.Contains(config, "npmauthident") {
				return nil, fmt.Errorf("Repository project contains package-manager credentials: %s", name)
			}
		}
		total += len(data)
		files[name] = string(data)
	}
	if _, err := io.Copy(io.Discard, limited); err != nil || limited.N <= 0 {
		return nil, fmt.Errorf("Repository archive is invalid or exceeds 64 MiB expanded")
	}
	return files, nil
}
