import { useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import {
  ArrowRight,
  ArrowUpRight,
  Braces,
  Check,
  ChevronRight,
  Code2,
  Database,
  File,
  FileCode2,
  FileImage,
  Folder,
  HardDrive,
  Layers3,
  Terminal,
} from "lucide-react";
import { Logo } from "./common";
import { LanguageSwitch } from "./language";
import { t } from "@/lib/i18n";
import "./home.css";

function WorkerPreview() {
  const [view, setView] = useState<"code" | "assets">("code");

  return (
    <div className="run-worker-preview">
      <div className="run-preview-tabs" aria-label={t("Workers 示例")}>
        <button type="button" aria-pressed={view === "code"} onClick={() => setView("code")}>
          <FileCode2 size={13} /> index.js
        </button>
        <button type="button" aria-pressed={view === "assets"} onClick={() => setView("assets")}>
          <Folder size={13} /> {t("静态资源")}
        </button>
      </div>
      {view === "code" ? (
        <pre className="run-worker-code">
          <code>
            <span className="run-code-purple">export default</span>
            {" {\n"}
            {"  async "}
            <span className="run-code-blue">fetch</span>
            {"(request, env) {\n"}
            {"    const url = new URL(request.url);\n\n"}
            {"    if (url.pathname === "}
            <span className="run-code-green">"/api/hello"</span>
            {") {\n"}
            {"      return Response.json({\n"}
            {"        message: "}
            <span className="run-code-green">"Hello, RUN!"</span>
            {"\n"}
            {"      });\n    }\n\n"}
            {"    return env."}
            <span className="run-code-green">ASSETS</span>
            {".fetch(request);\n"}
            {"  }\n};"}
          </code>
        </pre>
      ) : (
        <div className="run-asset-tree">
          <p>
            <Folder size={15} /> my-app/
          </p>
          <p className="run-tree-indent">
            <FileCode2 size={15} /> src/index.js <span>API</span>
          </p>
          <p className="run-tree-indent">
            <Folder size={15} /> dist/
          </p>
          <p className="run-tree-indent-deep">
            <FileCode2 size={15} /> index.html
          </p>
          <p className="run-tree-indent-deep">
            <Folder size={15} /> assets/
          </p>
          <p className="run-tree-indent">
            <File size={15} /> wrangler.jsonc
          </p>
          <div className="run-tree-note">
            <Check size={14} /> {t("页面与 API，一起部署。")}
          </div>
        </div>
      )}
    </div>
  );
}

function StoragePreview() {
  return (
    <div className="run-storage-preview">
      <div className="run-bucket-heading">
        <HardDrive size={15} />
        <span>my-assets</span>
        <span className="run-example-label">{t("示例")}</span>
      </div>
      <div className="run-file-row">
        <FileImage size={17} />
        <span>cover.webp</span>
        <span>240 KB</span>
      </div>
      <div className="run-file-row">
        <File size={17} />
        <span>readme.pdf</span>
        <span>1.2 MB</span>
      </div>
      <div className="run-file-row">
        <FileCode2 size={17} />
        <span>data.json</span>
        <span>8 KB</span>
      </div>
      <div className="run-storage-binding">
        <Braces size={14} />
        <code>env.FILES.get("cover.webp")</code>
      </div>
    </div>
  );
}

export function Home() {
  const { i18n } = useTranslation();

  useEffect(() => {
    document.title = "RUN · Run anything.";
    return () => {
      document.title = `RUN · ${t("控制台")}`;
    };
  }, [i18n.resolvedLanguage]);

  return (
    <div className="dark run-home">
      <a className="run-skip-link" href="#main">
        {t("跳到主要内容")}
      </a>
      <header className="run-header">
        <div className="run-container run-header-inner">
          <a href="/" aria-label={t("RUN 首页")} className="run-brand">
            <Logo dark />
          </a>
          <nav className="run-navigation" aria-label={t("首页导航")}>
            <a href="#products">{t("产品")}</a>
            <a href="#workflow">{t("开发与部署")}</a>
            <a href="/docs/">{t("文档")}</a>
          </nav>
          <div className="run-header-actions">
            <LanguageSwitch />
            <a className="run-button run-button-small run-button-secondary" href="/?page=login">
              {t("进入控制台")}
              <ArrowUpRight size={14} />
            </a>
          </div>
        </div>
      </header>

      <main id="main">
        <section className="run-hero run-container" aria-labelledby="run-title">
          <p className="run-eyebrow">
            <span />
            {t("从代码，到上线。")}
          </p>
          <h1 id="run-title">
            Run <span>anything.</span>
          </h1>
          <p className="run-hero-lead">{t("静态网站、API、完整应用。")}</p>
          <p className="run-hero-description">
            {t("用 Workers 运行代码，用 Storage 存储文件。")}
            <br className="run-desktop-break" />
            {t("让你的下一个想法，在 RUN 跑起来。")}
          </p>
          <div className="run-hero-actions">
            <a className="run-button run-button-primary" href="/?page=workers">
              {t("开始构建")}
              <ArrowRight size={16} />
            </a>
            <a className="run-button run-button-secondary" href="/docs/">
              {t("阅读文档")}
              <ArrowUpRight size={15} />
            </a>
          </div>
          <div className="run-hero-capabilities" aria-label={t("支持的应用类型")}>
            <span>
              <FileCode2 size={14} />
              {t("静态网站")}
            </span>
            <span>
              <Braces size={14} />
              API
            </span>
            <span>
              <Layers3 size={14} />
              {t("全栈应用")}
            </span>
          </div>
        </section>

        <section
          className="run-container run-products"
          id="products"
          aria-labelledby="run-products-title"
        >
          <div className="run-section-heading">
            <div>
              <p className="run-section-label">THE PLATFORM</p>
              <h2 id="run-products-title">{t("应用需要的，从这里开始。")}</h2>
            </div>
            <p>{t("运行代码，交付页面，存储文件。")}</p>
          </div>
          <div className="run-product-grid">
            <article className="run-product-card run-workers-card">
              <div className="run-card-topline">
                <Code2 size={25} />
                <span>COMPUTE</span>
              </div>
              <h3>Workers</h3>
              <p className="run-card-description">
                {t("静态页面与服务端逻辑，一个 Worker 搞定。构建网站、编写 API，或部署完整应用。")}
              </p>
              <WorkerPreview />
              <a className="run-card-link" href="/?page=workers">
                {t("部署你的第一个应用")}
                <ArrowRight size={16} />
              </a>
            </article>

            <article className="run-product-card run-storage-card">
              <div className="run-card-topline">
                <HardDrive size={25} />
                <span>STORAGE</span>
              </div>
              <h3>Storage</h3>
              <p className="run-card-description">
                {t("图片、文档与应用文件，放进存储桶。通过绑定，在 Worker 中直接读写。")}
              </p>
              <StoragePreview />
              <a className="run-card-link" href="/?page=buckets">
                {t("管理你的文件")}
                <ArrowRight size={16} />
              </a>
            </article>

            <article className="run-product-card run-database-card">
              <div className="run-card-topline">
                <Database size={25} />
                <span className="run-coming-soon">Coming soon</span>
              </div>
              <h3>Database</h3>
              <p className="run-card-description">
                {t("应用的数据，也将在这里安家。数据库能力即将加入 RUN。")}
              </p>
              <div className="run-database-preview" aria-hidden="true">
                <div className="run-database-orbit">
                  <Database size={46} strokeWidth={1} />
                </div>
                <div className="run-database-table">
                  <span>id</span>
                  <span>name</span>
                  <span>created_at</span>
                  {Array.from({ length: 9 }, (_, index) => (
                    <i key={index} />
                  ))}
                </div>
              </div>
              <p className="run-upcoming-note">{t("更多能力，正在路上。")}</p>
            </article>
          </div>
        </section>

        <section
          className="run-container run-workflow"
          id="workflow"
          aria-labelledby="run-workflow-title"
        >
          <div className="run-workflow-copy">
            <p className="run-section-label">BUILT FOR YOUR WORKFLOW</p>
            <h2 id="run-workflow-title">
              {t("写你熟悉的代码。")}
              <br />
              {t("运行你的下一个应用。")}
            </h2>
            <p>
              {t(
                "连接 Git 仓库，在控制台构建和发布；也可以使用 Wrangler，从终端部署。静态资源与 Worker 代码，一起交付。",
              )}
            </p>
            <a className="run-text-link" href="/docs/guides/cloudflare-workers.html">
              {t("查看部署指南")}
              <ArrowUpRight size={16} />
            </a>
            <div className="run-workflow-tools">
              <span>JavaScript</span>
              <span>TypeScript</span>
              <span>Git</span>
              <span>Wrangler</span>
            </div>
          </div>
          <div className="run-workflow-visual">
            <div className="run-deploy-window">
              <div className="run-window-bar">
                <span className="run-window-dots">
                  <i />
                  <i />
                  <i />
                </span>
                <span>
                  <Terminal size={13} />
                  hello-run
                </span>
                <span className="run-example-label">{t("部署示意")}</span>
              </div>
              <div className="run-deploy-body">
                <p className="run-terminal-comment">{t("配置好连接与项目后，发布到 RUN")}</p>
                <p>
                  <span className="run-prompt">$</span> npx wrangler deploy
                </p>
                <div className="run-deploy-steps">
                  <p>
                    <Check size={14} />
                    {t("构建应用代码")}
                  </p>
                  <p>
                    <Check size={14} />
                    {t("上传静态资源")}
                  </p>
                  <p>
                    <Check size={14} />
                    {t("发布 Worker 版本")}
                  </p>
                </div>
                <div className="run-deploy-result">
                  <span className="run-status-dot" />
                  {t("你的应用，已就绪。")}
                  <ArrowUpRight size={16} />
                </div>
              </div>
            </div>
            <div className="run-delivery-path">
              <span>
                <Code2 size={15} />
                Code
              </span>
              <ChevronRight size={13} />
              <span>
                <Layers3 size={15} />
                Build
              </span>
              <ChevronRight size={13} />
              <span className="run-delivery-final">
                RUN
                <ArrowUpRight size={14} />
              </span>
            </div>
          </div>
        </section>

        <section className="run-final-cta">
          <div className="run-container run-final-inner">
            <div>
              <h2>{t("下一个想法，现在开跑。")}</h2>
              <p>{t("一个页面，一个 API，或者一个全新的应用。")}</p>
            </div>
            <a className="run-button run-button-primary" href="/?page=workers">
              {t("开始构建")}
              <ArrowRight size={16} />
            </a>
          </div>
        </section>
      </main>

      <footer className="run-container run-footer">
        <a href="/" aria-label={t("RUN 首页")} className="run-brand">
          <Logo dark />
        </a>
        <span>Run anything.</span>
        <nav aria-label={t("页脚导航")}>
          <a href="/docs/guides/component-workers.html">Workers</a>
          <a href="/docs/guides/component-object-storage.html">Storage</a>
          <a href="/docs/">{t("文档")}</a>
        </nav>
      </footer>
    </div>
  );
}
