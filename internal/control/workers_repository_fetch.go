package control

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
const workerRepositoryProjectFilesMax = 4096
const workerRepositoryProjectBytesMax = 24 << 20
const workerRepositoryProjectFileMax = 8 << 20
const workerRepositoryProjectPathMax = 240

// extractRepositorySource retains the complete project directory needed by a
// Wrangler build. Assets are no longer peeled off before the build: Vite and
// framework adapters may generate both the entrypoint and assets at build time,
// while rules/find_additional_modules need non-JavaScript source files.
func extractRepositorySource(raw []byte, rootDirectory, entrypoint string) (map[string]string, map[string][]byte, error) {
	files, assets, err := extractArchive(raw, rootDirectory, nil, true)
	if err != nil {
		return nil, nil, err
	}
	// When the entrypoint is already known, it must be present in the tree.
	// Otherwise wrangler config supplies it and the caller validates after.
	if entrypoint != "" {
		if err := sourceEntryValid(entrypoint, files); err != nil {
			return nil, nil, fmt.Errorf("Repository project must include the configured JS or TS entrypoint")
		}
	}
	return files, assets, nil
}

func safeWorkerRepositoryProjectPath(name string) bool {
	if name == "" || len(name) > workerRepositoryProjectPathMax || len(path.Base(name)) > 100 || path.IsAbs(name) || path.Clean(name) != name || strings.HasPrefix(name, "../") || strings.Contains(name, "\\") {
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
func repositoryProjectFilePolicy(name string) (include bool, sensitive bool) {
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

// extractRepositoryAssets re-reads the archive for the resolved asset directory.
func extractRepositoryAssets(raw []byte, rootDirectory, directory string) (map[string][]byte, error) {
	_, assets, err := extractArchive(raw, rootDirectory, &WorkerAssetsConfig{Directory: directory}, false)
	return assets, err
}

func extractArchive(raw []byte, rootDirectory string, assets *WorkerAssetsConfig, collectSource bool) (map[string]string, map[string][]byte, error) {
	compressed, err := gzip.NewReader(bytes.NewReader(raw))
	if err != nil {
		return nil, nil, fmt.Errorf("Repository archive is not a valid tar.gz")
	}
	defer compressed.Close()
	limited := &io.LimitedReader{R: compressed, N: repositoryExpandedLimit + 1}
	archive := tar.NewReader(limited)
	files := make(map[string]string)
	assetFiles := make(map[string][]byte)
	prefix := ""
	total := 0
	assetTotal := 0
	for entries := 0; ; entries++ {
		header, err := archive.Next()
		if err == io.EOF {
			break
		}
		if err != nil || entries >= 10000 || limited.N <= 0 {
			return nil, nil, fmt.Errorf("Repository archive is invalid or exceeds extraction limits")
		}
		// GitHub puts commit metadata in a global PAX header before the repository root.
		if header.Typeflag == tar.TypeXGlobalHeader || header.Typeflag == tar.TypeXHeader {
			continue
		}
		name := strings.TrimSuffix(header.Name, "/")
		if name == "" || path.Clean(name) != name || strings.HasPrefix(name, "/") || strings.HasPrefix(name, "../") {
			return nil, nil, fmt.Errorf("Repository archive contains unsafe paths")
		}
		parts := strings.SplitN(name, "/", 2)
		if prefix == "" {
			prefix = parts[0]
		}
		if parts[0] != prefix {
			return nil, nil, fmt.Errorf("Repository archive has multiple roots")
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
			return nil, nil, fmt.Errorf("Repository project directory cannot contain symbolic links or submodules")
		}
		if header.Typeflag != tar.TypeReg {
			continue
		}
		if assets != nil && strings.HasPrefix(name, assets.Directory+"/") {
			asset := strings.TrimPrefix(name, assets.Directory+"/")
			if _, ok := workerAssetPath(asset); !ok || header.Size < 0 || header.Size > workerAssetMax || len(assetFiles) >= workerAssetsFilesMax {
				return nil, nil, fmt.Errorf("Repository assets must contain at most %d files of %d MiB", workerAssetsFilesMax, workerAssetMax>>20)
			}
			if _, ok := workerAssetType(asset); !ok {
				return nil, nil, fmt.Errorf("repository asset type is not supported: %s", asset)
			}
			data, err := io.ReadAll(archive)
			if err != nil || len(data) != int(header.Size) {
				return nil, nil, fmt.Errorf("repository asset is invalid: %s", asset)
			}
			assetTotal += len(data)
			if assetTotal > workerAssetsTotalMax {
				return nil, nil, fmt.Errorf("Repository assets exceed 20 MiB")
			}
			assetFiles[asset] = data
			continue
		}
		if !collectSource {
			continue
		}
		include, sensitive := repositoryProjectFilePolicy(name)
		if sensitive {
			return nil, nil, fmt.Errorf("Repository project contains a local credential or environment file: %s", name)
		}
		if !include {
			continue
		}
		if !safeWorkerRepositoryProjectPath(name) || header.Size < 0 || header.Size > workerRepositoryProjectFileMax || header.Size > int64(workerRepositoryProjectBytesMax-total) || len(files) >= workerRepositoryProjectFilesMax {
			return nil, nil, fmt.Errorf("Repository project must contain at most %d safe files and %d MiB; select a smaller project directory", workerRepositoryProjectFilesMax, workerRepositoryProjectBytesMax>>20)
		}
		if _, duplicate := files[name]; duplicate {
			return nil, nil, fmt.Errorf("Repository archive contains duplicate source paths")
		}
		data, err := io.ReadAll(archive)
		if err != nil || len(data) != int(header.Size) {
			return nil, nil, fmt.Errorf("Repository project file is invalid: %s", name)
		}
		switch strings.ToLower(path.Ext(name)) {
		case ".js", ".mjs", ".cjs", ".jsx", ".ts", ".tsx", ".json", ".jsonc", ".toml":
			if !utf8.Valid(data) {
				return nil, nil, fmt.Errorf("Repository project text file is not valid UTF-8: %s", name)
			}
		}
		if strings.EqualFold(path.Base(name), ".yarnrc.yml") {
			config := strings.ToLower(string(data))
			if strings.Contains(config, "npmauthtoken") || strings.Contains(config, "npmauthident") {
				return nil, nil, fmt.Errorf("Repository project contains package-manager credentials: %s", name)
			}
		}
		total += len(data)
		files[name] = string(data)
	}
	if _, err := io.Copy(io.Discard, limited); err != nil || limited.N <= 0 {
		return nil, nil, fmt.Errorf("Repository archive is invalid or exceeds 64 MiB expanded")
	}
	// Entrypoint and asset-completeness checks happen after the layout is
	// resolved: both may come from wrangler config, which is only readable once
	// the tree is in hand.
	return files, assetFiles, nil
}
