import { useState } from "react";
import { ArrowLeft, ArrowUpRight, BookOpen, Check, Copy, Terminal } from "lucide-react";
import { marked } from "marked";
import quickstart from "../../public/quickstart.md?raw";

const sections = [
  ["开始之前", "01"],
  ["1. 登录控制台", "02"],
  ["2. 安装 CLI", "03"],
  ["3. 连接你的账号", "04"],
  ["4. 认识 krun", "05"],
  ["接下来", "06"],
] as const;

export function Docs() {
  const [copied, setCopied] = useState(false);
  const [copyError, setCopyError] = useState(false);
  const origin = window.location.origin;
  const markdown = quickstart
    .replaceAll("{{KRUN_ORIGIN}}", origin)
    .replaceAll("](/", `](${origin}/`);
  const parts = markdown.split(/(?=^## )/m);

  async function copyMarkdown() {
    try {
      await navigator.clipboard.writeText(markdown);
      setCopyError(false);
      setCopied(true);
      window.setTimeout(() => setCopied(false), 2000);
    } catch {
      setCopyError(true);
    }
  }

  return (
    <div className="docs-shell min-h-screen">
      <header className="docs-header">
        <a href="/" className="docs-brand">
          <span className="docs-mark">k</span>
          <strong>krun</strong>
          <span className="docs-divider" />
          <span>文档</span>
        </a>
        <a href="/" className="docs-console">
          进入控制台 <ArrowUpRight size={15} />
        </a>
      </header>
      <div className="docs-layout">
        <aside className="docs-sidebar">
          <a href="/" className="docs-back">
            <ArrowLeft size={14} /> 返回控制台
          </a>
          <div className="docs-nav-label">开始使用</div>
          <a className="docs-nav-active" href="#top">
            <BookOpen size={16} /> Quick Start
          </a>
          <div className="docs-sidebar-note">
            从一个想法开始，
            <br />
            把它运行起来。<span>KRUN / DOCS</span>
          </div>
        </aside>
        <main className="docs-main" id="top">
          <div className="docs-breadcrumb">
            文档 <span>/</span> 开始使用 <span>/</span> Quick Start
          </div>
          <div className="docs-title-row">
            <div>
              <div className="docs-eyebrow">
                <span /> GETTING STARTED
              </div>
              <h1>
                Quick Start<span>.</span>
              </h1>
              <p>从登录控制台到连接 CLI，开始使用 krun。</p>
            </div>
            <div className="docs-actions">
              <a href="/quickstart.md" className="docs-source">查看原文 <ArrowUpRight size={14} /></a>
              <button className="docs-copy" onClick={() => void copyMarkdown()}>
                {copied ? <Check size={15} /> : <Copy size={15} />}
                {copied ? "已复制" : "复制 Markdown"}
              </button>
            </div>
          </div>
          {copyError && (
            <p role="alert" className="docs-copy-error">
              复制失败，请打开 Markdown 原文手动复制。
            </p>
          )}
          <div className="docs-intro">
            <span className="docs-intro-icon">
              <Terminal size={19} />
            </span>
            <div>
              <strong>准备好开始了吗？</strong>
              <p>一个控制台，连接你的团队与资源。跟随下面的步骤，几分钟内完成设置。</p>
            </div>
          </div>
          <article className="docs-article">
            {parts.slice(1).map((part, index) => (
              <section
                id={`step-${index}`}
                key={sections[index]?.[0]}
                dangerouslySetInnerHTML={{ __html: marked.parse(part, { async: false }) }}
              />
            ))}
          </article>
          <footer className="docs-footer">
            <span>KRUN DOCUMENTATION</span>
            <a href="/llms.txt">
              LLM 文档索引 <ArrowUpRight size={14} />
            </a>
          </footer>
        </main>
        <aside className="docs-toc">
          <div>本页内容</div>
          {sections.map(([title, number], index) => (
            <a href={`#step-${index}`} key={number}>
              <span>{number}</span>
              {title}
            </a>
          ))}
        </aside>
      </div>
    </div>
  );
}
