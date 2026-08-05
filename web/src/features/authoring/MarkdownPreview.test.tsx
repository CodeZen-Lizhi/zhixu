import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";

import { MarkdownPreview, safeMarkdownUrl } from "./MarkdownPreview";

describe("MarkdownPreview", () => {
  it("removes raw HTML, executable elements, remote images and dangerous URL protocols", () => {
    const { container } = render(<MarkdownPreview markdown={`# 安全预览

<script>window.compromised = true</script>
<img src="https://attacker.example/pixel" onerror="alert(1)">

[危险链接](javascript:alert(1))
![远程图片](https://attacker.example/track.png)

<a href="https://example.com" onclick="alert(1)">原始链接</a>
`} />);

    expect(screen.getByRole("heading", { name: "安全预览" })).toBeInTheDocument();
    expect(container.querySelector("script")).toBeNull();
    expect(container.querySelector("img")).toBeNull();
    expect(container.querySelector("[onerror], [onclick]")).toBeNull();
    expect(container.querySelector('a[href^="javascript:"]')).toBeNull();
    expect(screen.getByText("危险链接").tagName).toBe("SPAN");
    expect(screen.getByText("图片：远程图片")).toHaveClass("authoring-preview__image-placeholder");
    expect(screen.getByText("原始链接").closest("a")).toBeNull();
  });

  it("keeps only controlled external, mail and fragment links", () => {
    render(<MarkdownPreview markdown={`[官网](https://example.com/docs?q=1)
[邮件](mailto:owner@example.com)
[章节](#section)
[相对路径](../private.md)`} />);

    expect(screen.getByRole("link", { name: "官网" })).toMatchObject({
      target: "_blank",
      rel: "noopener noreferrer",
    });
    expect(screen.getByRole("link", { name: "邮件" })).toHaveAttribute("href", "mailto:owner@example.com");
    expect(screen.getByRole("link", { name: "章节" })).toHaveAttribute("href", "#section");
    expect(screen.getByText("相对路径").tagName).toBe("SPAN");
  });

  it("rejects encoded and whitespace-obfuscated protocols in the URL transform", () => {
    expect(safeMarkdownUrl(" javascript:alert(1) ")).toBe("");
    expect(safeMarkdownUrl("data:text/html,boom")).toBe("");
    expect(safeMarkdownUrl("file:///etc/passwd")).toBe("");
    expect(safeMarkdownUrl("#knowledge-point")).toBe("#knowledge-point");
    expect(safeMarkdownUrl("https://example.com/path")).toBe("https://example.com/path");
  });
});
