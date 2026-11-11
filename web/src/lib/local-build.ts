import { apiForm } from "./api.ts";
import { t } from "./i18n.ts";
import type { Build } from "./workers.ts";

export type LocalProject = { name: string; form: FormData };
export type LocalBuild = { build: Build; kind: "static" | "worker" };

const ignored = new Set([".git", "node_modules", ".wrangler"]);

export function localProjectFromFiles(files: File[]): LocalProject {
  const form = new FormData();
  const first = files[0];
  if (files.length === 1 && !first.webkitRelativePath && /\.zip$/i.test(first.name)) {
    form.append("archive", first, first.name);
    return { name: first.name, form };
  }

  const name = first?.webkitRelativePath.split("/")[0];
  if (!name) throw new Error(t("请选择一个项目文件夹或 ZIP 压缩包。"));

  for (const file of files) {
    const [root, ...parts] = file.webkitRelativePath.split("/");
    if (root !== name || !parts.length) {
      throw new Error(t("请选择一个项目文件夹或 ZIP 压缩包。"));
    }
    if (!parts.some((part) => ignored.has(part))) {
      form.append("file:" + parts.join("/"), file, file.name);
    }
  }
  return directoryProject(name, form);
}

export async function localProjectFromDrop(
  transfer: DataTransfer,
  signal?: AbortSignal,
): Promise<LocalProject> {
  const items = Array.from(transfer.items).filter((item) => item.kind === "file");
  if (items.length > 1) throw new Error(t("请选择一个项目文件夹或 ZIP 压缩包。"));
  const entry = items[0]?.webkitGetAsEntry?.();
  if (!entry?.isDirectory) return localProjectFromFiles(Array.from(transfer.files));

  const form = new FormData();
  await appendDirectory(form, entry as FileSystemDirectoryEntry, "", signal);
  return directoryProject(entry.name, form);
}

async function appendDirectory(
  form: FormData,
  directory: FileSystemDirectoryEntry,
  prefix: string,
  signal?: AbortSignal,
): Promise<void> {
  const reader = directory.createReader();
  for (;;) {
    signal?.throwIfAborted();
    const entries = await new Promise<FileSystemEntry[]>((resolve, reject) => {
      reader.readEntries(resolve, reject);
    });
    if (entries.length === 0) return;

    for (const entry of entries) {
      signal?.throwIfAborted();
      if (ignored.has(entry.name)) continue;
      const path = prefix + entry.name;
      if (entry.isDirectory) {
        await appendDirectory(form, entry as FileSystemDirectoryEntry, path + "/", signal);
      } else if (entry.isFile) {
        const file = await new Promise<File>((resolve, reject) => {
          (entry as FileSystemFileEntry).file(resolve, reject);
        });
        form.append("file:" + path, file, file.name);
      }
    }
  }
}

function directoryProject(name: string, form: FormData): LocalProject {
  if (form.keys().next().done) throw new Error(t("此文件夹没有可上传的文件。"));
  return { name, form };
}

export function submitLocalBuild(
  path: string,
  project: LocalProject,
  root: string,
  key: string,
  signal?: AbortSignal,
): Promise<LocalBuild> {
  const query = root ? "?" + new URLSearchParams({ project_root: root }).toString() : "";
  return apiForm<LocalBuild>(path + query, project.form, key, signal);
}
