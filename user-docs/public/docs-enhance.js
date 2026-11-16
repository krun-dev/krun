const article = document.querySelector(".sl-markdown-content");
const source = document.querySelector("[data-doc-source]");
const copy = document.querySelector("[data-doc-copy]");
const error = document.querySelector("[data-doc-error]");
const origin = window.location.origin;

if (article) {
  const dialog = document.createElement("dialog");
  dialog.className = "krun-image-preview";
  dialog.setAttribute("aria-label", "图片预览");
  const close = document.createElement("button");
  close.type = "button";
  close.className = "krun-image-preview-close";
  close.setAttribute("aria-label", "关闭图片预览");
  close.textContent = "×";
  const preview = document.createElement("img");
  dialog.append(close, preview);
  document.body.append(dialog);
  close.addEventListener("click", () => dialog.close());
  dialog.addEventListener("click", (event) => {
    if (event.target === dialog) dialog.close();
  });
  for (const image of article.querySelectorAll("img:not(a img)")) {
    image.tabIndex = 0;
    image.setAttribute("role", "button");
    image.setAttribute("aria-label", `放大图片：${image.alt}`);
    image.addEventListener("keydown", (event) => {
      if (event.key === "Enter" || event.key === " ") {
        event.preventDefault();
        image.click();
      }
    });
  }
  article.addEventListener("click", (event) => {
    if (event.button !== 0 || event.metaKey || event.ctrlKey || event.shiftKey || event.altKey) return;
    const image = event.target.closest("img");
    if (!image) return;
    const link = image.closest("a");
    const url = new URL(link ? link.href : image.currentSrc || image.src, location.href);
    if (url.origin !== origin || !/\.(png|jpe?g|gif|webp|svg)$/i.test(url.pathname)) return;
    event.preventDefault();
    preview.src = url.href;
    preview.alt = image.alt;
    dialog.showModal();
  });
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
