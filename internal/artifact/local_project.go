package artifact

import (
	"archive/zip"
	"bytes"
	"fmt"
	"io"
	"io/fs"
	"path"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/krun-dev/krun/internal/domain"
)

const LocalProjectUploadMax = 32 << 20

type LocalProject struct {
	Kind               string
	ConfigPath         string
	Entrypoint         string
	CompatibilityDate  string
	CompatibilityFlags []string
	Files              map[string]string
}

// PrepareLocalProject selects and validates an uploaded project for the Wrangler
// builder. An explicit root is relative to the input, before any automatic
// directory unwrapping.
func PrepareLocalProject(archive []byte, files map[string][]byte, root string) (LocalProject, error) {
	var project LocalProject
	if (archive == nil) == (files == nil) {
		return project, invalidLocalProject("Upload either one ZIP archive or a directory of files")
	}
	var err error
	if archive != nil {
		files, err = readLocalProjectZIP(archive)
	} else {
		files, err = validateLocalProjectFiles(files)
	}
	if err != nil {
		return project, err
	}
	root, err = resolveLocalProjectRoot(files, root)
	if err != nil {
		return project, err
	}

	project.Files, err = selectLocalProjectFiles(files, root)
	if err != nil {
		return LocalProject{}, err
	}
	config, configPath, err := ParseWranglerConfig(project.Files)
	if err != nil {
		return LocalProject{}, invalidLocalProject("Invalid Wrangler configuration: %v", err)
	}
	project.Kind = "worker"
	if config == nil {
		project.Files, err = prepareStaticProject(project.Files)
		if err != nil {
			return LocalProject{}, err
		}
		config, configPath, err = ParseWranglerConfig(project.Files)
		if err != nil {
			return LocalProject{}, err
		}
		project.Kind = "static"
	}
	project.ConfigPath = configPath
	project.Entrypoint, err = validateLocalWranglerBuild(config, project.Files)
	if err != nil {
		return LocalProject{}, err
	}
	project.CompatibilityDate, project.CompatibilityFlags, err = ResolveWranglerCompatibility(config)
	if err != nil {
		return LocalProject{}, invalidLocalProject("Invalid Worker compatibility configuration: %v", err)
	}
	return project, nil
}

func readLocalProjectZIP(raw []byte) (map[string][]byte, error) {
	if len(raw) > LocalProjectUploadMax {
		return nil, invalidLocalProject("ZIP upload exceeds %d MiB", LocalProjectUploadMax>>20)
	}
	archive, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		return nil, invalidLocalProject("Upload is not a valid ZIP archive with safe relative paths")
	}
	if len(archive.File) > RepositoryFileLimit {
		return nil, invalidLocalProject("ZIP archive exceeds %d entries", RepositoryFileLimit)
	}

	files := make(map[string][]byte)
	entries := make(map[string]bool, len(archive.File))
	var remaining uint64 = RepositoryExpandedLimit
	for _, file := range archive.File {
		name := strings.TrimSuffix(file.Name, "/")
		mode := file.Mode().Type()
		if mode != 0 && mode != fs.ModeDir || mode == fs.ModeDir && file.UncompressedSize64 != 0 {
			return nil, invalidLocalProject("ZIP archive can contain only regular files and directories")
		}
		if !safeLocalProjectPath(name) {
			return nil, invalidLocalProject("ZIP archive contains an unsafe path: %q", file.Name)
		}
		if _, exists := entries[name]; exists {
			return nil, invalidLocalProject("ZIP archive contains duplicate paths: %q", name)
		}
		entries[name] = mode == fs.ModeDir
		if file.UncompressedSize64 > remaining {
			return nil, invalidLocalProject("ZIP archive exceeds %d MiB expanded", RepositoryExpandedLimit>>20)
		}
		remaining -= file.UncompressedSize64
		if mode == fs.ModeDir {
			continue
		}

		include, err := includeLocalProjectFile(name)
		if err != nil {
			return nil, err
		}
		if !include {
			continue
		}
		if file.UncompressedSize64 > WorkerRepositoryProjectFileMax {
			return nil, invalidLocalProject("Project file %q exceeds %d MiB", name, WorkerRepositoryProjectFileMax>>20)
		}
		reader, err := file.Open()
		if err != nil {
			return nil, invalidLocalProject("Invalid ZIP file: %q", name)
		}
		data, err := io.ReadAll(io.LimitReader(reader, int64(file.UncompressedSize64)+1))
		reader.Close()
		if err != nil || uint64(len(data)) != file.UncompressedSize64 {
			return nil, invalidLocalProject("Invalid ZIP file: %q", name)
		}
		files[name] = data
	}
	if err := validateLocalProjectTree(entries); err != nil {
		return nil, err
	}
	return files, nil
}

func validateLocalProjectFiles(input map[string][]byte) (map[string][]byte, error) {
	if len(input) > RepositoryFileLimit {
		return nil, invalidLocalProject("Directory upload exceeds %d entries", RepositoryFileLimit)
	}
	files := make(map[string][]byte, len(input))
	entries := make(map[string]bool, len(input))
	total := 0
	for name, data := range input {
		if !safeLocalProjectPath(name) {
			return nil, invalidLocalProject("Directory upload contains an unsafe path: %q", name)
		}
		entries[name] = false
		if len(data) > LocalProjectUploadMax-total {
			return nil, invalidLocalProject("Directory upload exceeds %d MiB", LocalProjectUploadMax>>20)
		}
		total += len(data)
		include, err := includeLocalProjectFile(name)
		if err != nil {
			return nil, err
		}
		if include {
			files[name] = data
		}
	}
	if err := validateLocalProjectTree(entries); err != nil {
		return nil, err
	}
	return files, nil
}

func safeLocalProjectPath(name string) bool {
	return name != "" && name != "." && name != ".." && len(name) <= WorkerRepositoryProjectPathMax &&
		utf8.ValidString(name) && !path.IsAbs(name) && path.Clean(name) == name &&
		!strings.HasPrefix(name, "../") && !strings.ContainsAny(name, "\\:") &&
		strings.IndexFunc(name, func(r rune) bool { return r < 0x20 || r == 0x7f }) == -1
}

func includeLocalProjectFile(name string) (bool, error) {
	include, sensitive := RepositoryProjectFilePolicy(name)
	if sensitive || include && strings.HasPrefix(strings.ToLower(path.Base(name)), ".dev.vars.") {
		return false, invalidLocalProject("Project contains a local credential or environment file: %q", name)
	}
	// Finder adds these metadata files beside the contents of a ZIP archive.
	if path.Base(name) == ".DS_Store" || strings.HasPrefix(name, "__MACOSX/") {
		return false, nil
	}
	return include, nil
}

func validateLocalProjectTree(entries map[string]bool) error {
	for name := range entries {
		for parent := path.Dir(name); parent != "."; parent = path.Dir(parent) {
			if directory, exists := entries[parent]; exists && !directory {
				return invalidLocalProject("Project file conflicts with a directory: %q", parent)
			}
		}
	}
	return nil
}

func resolveLocalProjectRoot(files map[string][]byte, requested string) (string, error) {
	if requested != "" {
		if requested == "." {
			return "", nil
		}
		if !safeLocalProjectPath(requested) {
			return "", invalidLocalProject("Project root must be a safe relative directory")
		}
		return requested, nil
	}
	if len(files) == 0 {
		return "", invalidLocalProject("Upload contains no project files")
	}

	var common string
	for name := range files {
		folder, _, nested := strings.Cut(name, "/")
		if !nested || common != "" && common != folder {
			common = ""
			break
		}
		common = folder
	}
	prefix := ""
	if common != "" {
		prefix = common + "/"
	}
	for _, config := range WranglerFileNames {
		if _, found := files[prefix+config]; found {
			return common, nil
		}
	}
	if _, found := files[prefix+"index.html"]; found {
		return common, nil
	}
	roots := make(map[string]bool)
	for name := range files {
		if slices.Contains(WranglerFileNames, path.Base(name)) {
			roots[path.Dir(name)] = true
		} else if path.Base(name) == "index.html" {
			root := path.Dir(name)
			if _, exists := roots[root]; !exists {
				roots[root] = false
			}
		}
	}
	candidates := make([]string, 0, len(roots))
	for root, isWorker := range roots {
		// A nested page belongs to its nearest project rather than creating
		// a separate static project beside it.
		nested := false
		if !isWorker {
			for parent := path.Dir(root); parent != "."; parent = path.Dir(parent) {
				if _, exists := roots[parent]; exists {
					nested = true
					break
				}
			}
		}
		if !nested {
			candidates = append(candidates, root)
		}
	}
	switch len(candidates) {
	case 0:
		return common, nil
	case 1:
		return candidates[0], nil
	}
	slices.Sort(candidates)
	visible := candidates
	const candidateLimit = 12
	if len(visible) > candidateLimit {
		visible = visible[:candidateLimit]
	}
	message := "Multiple projects found. Set the project root to one of these upload-relative directories: " + strings.Join(visible, ", ")
	if len(candidates) > len(visible) {
		message += fmt.Sprintf(" (and %d more)", len(candidates)-len(visible))
	}
	return "", domain.Invalid("LOCAL_PROJECT_ROOT_REQUIRED", message)
}

func selectLocalProjectFiles(files map[string][]byte, root string) (map[string]string, error) {
	selected := make(map[string]string)
	prefix := ""
	if root != "" {
		prefix = root + "/"
	}
	total := 0
	for fullName, data := range files {
		name, inRoot := strings.CutPrefix(fullName, prefix)
		if !inRoot {
			continue
		}
		if strings.IndexFunc(name, func(r rune) bool { return r > 0x7e }) != -1 {
			return nil, invalidLocalProject("File names inside the project must use printable ASCII: %q", name)
		}
		if !WorkerUSTARPathValid("src/" + name) {
			return nil, invalidLocalProject("Project file path exceeds the portable archive limit: %q", name)
		}
		if len(data) > WorkerRepositoryProjectFileMax {
			return nil, invalidLocalProject("Project file %q exceeds %d MiB", name, WorkerRepositoryProjectFileMax>>20)
		}
		total += len(data)
		if len(selected) >= WorkerRepositoryProjectFilesMax || total > WorkerRepositoryProjectBytesMax {
			return nil, invalidLocalProject("Project must contain at most %d files and %d MiB", WorkerRepositoryProjectFilesMax, WorkerRepositoryProjectBytesMax>>20)
		}
		content := string(data)
		if strings.EqualFold(path.Base(name), ".yarnrc.yml") {
			lower := strings.ToLower(content)
			if strings.Contains(lower, "npmauthtoken") || strings.Contains(lower, "npmauthident") {
				return nil, invalidLocalProject("Project contains package-manager credentials: %q", name)
			}
		}
		selected[name] = content
	}
	if len(selected) == 0 {
		return nil, invalidLocalProject("No project files found in the selected directory")
	}
	return selected, nil
}

func localProjectBuildConfig(name string) bool {
	if strings.ContainsRune(name, '/') {
		return false
	}
	switch name {
	case "package.json", "package-lock.json", "pnpm-lock.yaml", "yarn.lock", "bun.lock", "bun.lockb", "angular.json", "tsconfig.json":
		return true
	}
	tool, _, isConfig := strings.Cut(name, ".config.")
	if isConfig {
		switch tool {
		case "vite", "next", "nuxt", "astro", "svelte", "vue", "webpack", "rollup", "rspack", "remix":
			return true
		}
	}
	return false
}

func prepareStaticProject(files map[string]string) (map[string]string, error) {
	index, hasIndex := files["index.html"]
	if strings.Contains(index, "/@vite/client") {
		return nil, invalidLocalProject("Upload the static build output directory or include a Wrangler configuration")
	}
	for name := range files {
		source := false
		switch strings.ToLower(path.Ext(name)) {
		case ".ts", ".mts", ".cts", ".tsx", ".jsx", ".vue", ".svelte", ".astro":
			source = true
		}
		if source || localProjectBuildConfig(name) {
			return nil, invalidLocalProject("Upload the static build output directory or include a Wrangler configuration (found %q)", name)
		}
	}
	if !hasIndex {
		return nil, invalidLocalProject("The project directory must contain a Wrangler configuration or a built static index.html")
	}
	if len(files) > WorkerAssetsFilesMax {
		return nil, invalidLocalProject("Static project exceeds %d files", WorkerAssetsFilesMax)
	}

	const assetsDirectory = "public"
	staged := make(map[string]string, len(files)+1)
	staged["wrangler.jsonc"] = fmt.Sprintf(`{"name":"krun-static-upload","compatibility_date":%q,"assets":{"directory":%q}}`, WorkerDefaultCompatibilityDate, assetsDirectory)
	total := 0
	for name, content := range files {
		stagedName := assetsDirectory + "/" + name
		if _, ok := WorkerAssetPath(name); !ok || !safeLocalProjectPath(stagedName) || !WorkerUSTARPathValid("src/"+stagedName) {
			return nil, invalidLocalProject("Invalid static asset path: %q", name)
		}
		if len(content) > WorkerAssetMax {
			return nil, invalidLocalProject("Static asset %q exceeds %d MiB", name, WorkerAssetMax>>20)
		}
		total += len(content)
		if total > WorkerAssetsTotalMax {
			return nil, invalidLocalProject("Static assets exceed %d MiB", WorkerAssetsTotalMax>>20)
		}
		staged[stagedName] = content
	}

	ignore := files[".assetsignore"]
	if len(ignore) > 64<<10 || !utf8.ValidString(ignore) {
		return nil, invalidLocalProject(".assetsignore must be valid UTF-8 and at most 64 KiB")
	}
	if err := ValidateAssetRules(&domain.WorkerAssetsConfig{Headers: files["_headers"], Redirects: files["_redirects"]}); err != nil {
		return nil, err
	}
	return staged, nil
}

func validateLocalWranglerBuild(config *WranglerConfig, files map[string]string) (string, error) {
	assets, entrypoint, err := ResolveWranglerBuild(config)
	if err != nil {
		return "", invalidLocalProject("Invalid Wrangler configuration: %v", err)
	}
	if entrypoint != "" && !safeLocalProjectPath(entrypoint) {
		return "", invalidLocalProject("Wrangler main must be a safe relative path")
	}
	if config.Assets != nil {
		if assets.Directory != "" && !safeLocalProjectPath(assets.Directory) {
			return "", invalidLocalProject("Wrangler assets.directory must be a safe relative directory")
		}
		if err := ValidateWranglerInputAssets(assets); err != nil {
			return "", err
		}
	}
	if entrypoint == "" && assets.Directory == "" {
		if _, hasPackage := files["package.json"]; !hasPackage {
			return "", invalidLocalProject("Wrangler configuration must declare main or assets, or the project must declare a build in package.json")
		}
	}
	return entrypoint, nil
}

func invalidLocalProject(format string, args ...any) error {
	return domain.Invalid("LOCAL_PROJECT_INVALID", fmt.Sprintf(format, args...))
}
