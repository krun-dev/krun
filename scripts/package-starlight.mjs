import { createHash } from "node:crypto";
import { cp, mkdir, readdir, readFile, writeFile } from "node:fs/promises";
import path from "node:path";
import { parse, serialize } from "parse5";

const source = "dist/starlight";
const destination = "dist/console";
const assetDir = path.join(destination, "_astro", "csp");
const digest = (value) => createHash("sha256").update(value).digest("hex").slice(0, 20);
await mkdir(assetDir, { recursive: true });

async function hardenHtml(html) {
  const styles = new Map();
  const tree = parse(html);
  let head;
  async function walk(node) {
    if (node.tagName === "head") head = node;
    if (node.tagName === "html") {
      const theme = node.attrs.find((attr) => attr.name === "data-theme");
      if (theme) theme.value = "light";
      else node.attrs.push({ name: "data-theme", value: "light" });
    }
    if (node.attrs) {
      const attrs = node.attrs;
      const style = attrs.find((attr) => attr.name === "style");
      if (style) {
        node.attrs = attrs.filter((attr) => attr !== style);
        // Starlight removes this initial inline style after detecting keyboard shortcuts.
        if (style.value !== "display: none;") {
          const name = `krun-csp-${digest(style.value)}`;
          styles.set(name, style.value);
          const existing = node.attrs.find((attr) => attr.name === "class");
          if (existing) existing.value += ` ${name}`;
          else node.attrs.push({ name: "class", value: name });
        }
      }
      if (node.tagName === "script" && !attrs.some((attr) => attr.name === "src")) {
        const type = attrs.find((attr) => attr.name === "type")?.value ?? "";
        const body = (node.childNodes ?? []).map((child) => child.value ?? "").join("");
        if (
          body.trim() &&
          ["", "module", "text/javascript", "application/javascript"].includes(type)
        ) {
          const name = `${digest(body)}.js`;
          await writeFile(path.join(assetDir, name), body);
          node.attrs.push({ name: "src", value: `/_astro/csp/${name}` });
          node.childNodes = [];
        }
      }
    }
    if (node.childNodes) {
      // The build origin is deliberately not a real deployment URL.
      node.childNodes = node.childNodes.filter((child) => {
        const attr = (name) => child.attrs?.find((item) => item.name === name)?.value;
        return (
          !(child.tagName === "link" && ["canonical", "sitemap"].includes(attr("rel"))) &&
          !(child.tagName === "meta" && attr("property") === "og:url")
        );
      });
      for (const child of node.childNodes) await walk(child);
    }
    if (node.content) await walk(node.content);
  }
  await walk(tree);
  if (!head) throw new Error("Starlight HTML has no head element");
  const css = [...styles].map(([name, value]) => `.${name}{${value}}`).join("\n");
  const stylesheet = `${digest(css)}.css`;
  await writeFile(path.join(assetDir, stylesheet), css);
  const link = {
    nodeName: "link",
    tagName: "link",
    namespaceURI: "http://www.w3.org/1999/xhtml",
    attrs: [
      { name: "rel", value: "stylesheet" },
      { name: "href", value: `/_astro/csp/${stylesheet}` },
    ],
    childNodes: [],
    parentNode: head,
  };
  head.childNodes.push(link);
  return serialize(tree);
}

async function copyOutput(relative = "") {
  for (const entry of await readdir(path.join(source, relative), { withFileTypes: true })) {
    const item = path.join(relative, entry.name);
    if (entry.isDirectory()) {
      await mkdir(path.join(destination, item), { recursive: true });
      await copyOutput(item);
    } else if (entry.isFile()) {
      if (["404.html", "index.html"].includes(item) || item.startsWith("sitemap")) continue;
      const target = path.join(destination, item);
      if (item.endsWith(".html")) {
        const html = await hardenHtml(await readFile(path.join(source, item), "utf8"));
        await writeFile(target, html);
        if (item === "docs.html") await writeFile(path.join(destination, "docs"), html);
      } else if (item.startsWith("llms") && item.endsWith(".txt")) {
        const text = await readFile(path.join(source, item), "utf8");
        await writeFile(target, text.replaceAll("https://krun.invalid/", "/"));
      } else {
        await cp(path.join(source, item), target);
        // Browsers display text/plain inline, while .md remains available to tools.
        if (item.endsWith(".md")) await cp(path.join(source, item), `${target}.txt`);
      }
    }
  }
}
await copyOutput();
// Preserve the original Quick Start Markdown URL.
await cp(path.join(destination, "docs.md"), path.join(destination, "quickstart.md"));
