package artifact

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
)

type UploadedModule struct {
	Name, Type string
	Data       []byte
}

// PackModules accepts already bundled modules. It never evaluates uploaded code.
// ParseWorkerArchive remains the single validator for paths, types and limits.
func PackModules(main, date string, flags []string, modules []UploadedModule, assets *WorkerManifestAssets, files map[string][]byte) ([]byte, error) {
	var err error
	date, flags, err = NormalizeWorkerCompatibility(date, flags)
	if err != nil {
		return nil, err
	}
	if main == "" || main == "__krun_entrypoint__.mjs" {
		return nil, errors.New("invalid main module")
	}
	found := false
	for _, m := range modules {
		if m.Name == main {
			found = m.Type == "esm"
		}
		if m.Name == "__krun_entrypoint__.mjs" {
			return nil, errors.New("reserved module name")
		}
	}
	if !found {
		return nil, errors.New("main module must be an ES module")
	}
	quoted, _ := json.Marshal("./" + main)
	wrapper := []byte(fmt.Sprintf("import worker from %s;export * from %s;import {WorkerEntrypoint} from 'cloudflare:workers';export default worker;export class KrunValidation extends WorkerEntrypoint {ping(){if(!worker || (typeof worker.fetch!=='function'&&typeof worker?.prototype?.fetch!=='function'))throw new Error('Default export must provide fetch');return {ok:true}}}", quoted, quoted))
	modules = append(append([]UploadedModule(nil), modules...), UploadedModule{Name: "__krun_entrypoint__.mjs", Type: "esm", Data: wrapper})
	sort.Slice(modules, func(i, j int) bool { return modules[i].Name < modules[j].Name })
	manifest := WorkerManifest{Schema: 2, Profile: WorkerProfile, CompatibilityDate: date, CompatibilityFlags: flags, MainModule: "__krun_entrypoint__.mjs", Assets: assets}
	entries := map[string][]byte{}
	for _, m := range modules {
		if _, ok := entries["modules/"+m.Name]; ok {
			return nil, errors.New("duplicate module")
		}
		manifest.Modules = append(manifest.Modules, WorkerManifestModule{Name: m.Name, Path: "modules/" + m.Name, Type: m.Type, Digest: BytesDigest(m.Data), Size: len(m.Data)})
		entries["modules/"+m.Name] = m.Data
	}
	for name, data := range files {
		entries["assets/"+name] = data
	}
	raw, err := json.Marshal(manifest)
	if err != nil {
		return nil, err
	}
	entries["manifest.json"] = raw
	keys := make([]string, 0, len(entries))
	for name := range entries {
		keys = append(keys, name)
	}
	sort.Strings(keys)
	var out bytes.Buffer
	gz := gzip.NewWriter(&out)
	tw := tar.NewWriter(gz)
	for _, name := range keys {
		data := entries[name]
		if err = tw.WriteHeader(&tar.Header{Name: name, Mode: 0600, Size: int64(len(data)), Typeflag: tar.TypeReg, Format: tar.FormatUSTAR}); err != nil {
			return nil, err
		}
		if _, err = tw.Write(data); err != nil {
			return nil, err
		}
	}
	if err = tw.Close(); err != nil {
		return nil, err
	}
	if err = gz.Close(); err != nil {
		return nil, err
	}
	if _, _, err = ParseWorkerArchive(out.Bytes()); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}
