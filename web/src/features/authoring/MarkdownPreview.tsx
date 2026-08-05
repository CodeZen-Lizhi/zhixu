import type { ComponentPropsWithoutRef } from "react";
import ReactMarkdown from "react-markdown";
import remarkGfm from "remark-gfm";

const allowedProtocols = new Set(["http:", "https:", "mailto:"]);

export const safeMarkdownUrl = (url: string): string => {
  const candidate = url.trim();
  if (candidate.startsWith("#")) return candidate;
  try {
    const parsed = new URL(candidate);
    return allowedProtocols.has(parsed.protocol) ? parsed.href : "";
  } catch {
    return "";
  }
};

const SafeLink = ({ href, children, ...props }: ComponentPropsWithoutRef<"a">) => {
  if (href === undefined || href === "") return <span>{children}</span>;
  const external = href.startsWith("http://") || href.startsWith("https://");
  return <a
    {...props}
    href={href}
    {...(external ? { target: "_blank", rel: "noopener noreferrer" } : {})}
  >{children}</a>;
};

const SafeImage = ({ alt }: ComponentPropsWithoutRef<"img">) => <span className="authoring-preview__image-placeholder">{alt === undefined || alt === "" ? "图片" : `图片：${alt}`}</span>;

export const MarkdownPreview = ({ markdown }: { markdown: string }) => (
  <article className="authoring-preview__content" aria-label="Markdown 预览">
    {markdown.trim() === "" ? <p className="authoring-preview__empty">正文预览会显示在这里。</p> : <ReactMarkdown
      components={{ a: SafeLink, img: SafeImage }}
      remarkPlugins={[remarkGfm]}
      skipHtml
      urlTransform={safeMarkdownUrl}
    >{markdown}</ReactMarkdown>}
  </article>
);
