package artifact

import "testing"

func TestUploadedModulesUseArchiveValidator(t *testing.T) {
	modules := []UploadedModule{{Name: "nested/index.mjs", Type: "esm", Data: []byte("export default {fetch(){return new Response('ok')}}")}}
	raw, err := PackModules("nested/index.mjs", "", nil, modules, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	code, _, err := ParseWorkerArchive(raw)
	if err != nil || len(code.Modules) != 2 {
		t.Fatalf("code: %#v %v", code, err)
	}
	for _, name := range []string{"../escape.mjs", "/absolute.mjs", "__krun_entrypoint__.mjs"} {
		if _, err := PackModules(name, "2026-09-28", nil, []UploadedModule{{Name: name, Type: "esm", Data: modules[0].Data}}, nil, nil); err == nil {
			t.Fatalf("unsafe module %q accepted", name)
		}
	}
}
