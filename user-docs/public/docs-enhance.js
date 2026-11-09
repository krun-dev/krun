const article = document.querySelector(".sl-markdown-content");
const source = document.querySelector("[data-doc-source]");
const copy = document.querySelector("[data-doc-copy]");
const error = document.querySelector("[data-doc-error]");
const origin = window.location.origin;

if (article) {
  const nodes = document.createTreeWalker(article, NodeFilter.SHOW_TEXT);
  while (nodes.nextNode()) {
    nodes.currentNode.textContent = nodes.currentNode.textContent.replaceAll(
      "{{KRUN_ORIGIN}}",
      origin,
    );
  }
  for (const button of article.querySelectorAll(".expressive-code button[data-code]")) {
    button.dataset.code = button.dataset.code.replaceAll("{{KRUN_ORIGIN}}", origin);
  }
}
if (copy && source) {
  copy.hidden = false;
  copy.addEventListener("click", async () => {
    try {
      const response = await fetch(source.href);
      if (!response.ok) throw new Error("Markdown unavailable");
      const markdown = (await response.text())
        .replaceAll("{{KRUN_ORIGIN}}", origin)
        .replaceAll("](/", `](${origin}/`);
      await navigator.clipboard.writeText(markdown);
      error.hidden = true;
      copy.textContent = "已复制";
      window.setTimeout(() => {
        copy.textContent = "复制 Markdown";
      }, 2000);
    } catch {
      error.hidden = false;
    }
  });
}
