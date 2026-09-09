import { Component, useEffect, useId, useRef, useState, type ReactNode } from "react";
import type { RootOptions } from "react-dom/client";

import { Button } from "../shared/ui";

interface RouteContentBoundaryProps {
  children: ReactNode;
  resetKey: string;
  onReload?: () => void;
}

interface RouteErrorBoundaryProps extends RouteContentBoundaryProps {
  onRetry: () => void;
  onReload: () => void;
}

interface RouteErrorBoundaryState {
  resetKey: string;
  failed: boolean;
}

const RouteRecovery = ({ onRetry, onReload }: Pick<RouteErrorBoundaryProps, "onRetry" | "onReload">) => {
  const titleId = useId();
  const retryButton = useRef<HTMLButtonElement>(null);

  useEffect(() => {
    retryButton.current?.focus();
  }, []);

  return <section className="ui-state ui-state--error" role="alert" aria-labelledby={titleId}>
    <strong id={titleId}>页面暂时无法显示</strong>
    <p>可以先重试页面；若仍然失败，请刷新页面重新加载资源。也可以从主导航打开其他页面。</p>
    <div className="button-row">
      <Button ref={retryButton} type="button" onClick={onRetry}>重试页面</Button>
      <Button type="button" variant="secondary" onClick={onReload}>刷新页面</Button>
    </div>
  </section>;
};

class RouteErrorBoundary extends Component<RouteErrorBoundaryProps, RouteErrorBoundaryState> {
  override state: RouteErrorBoundaryState = { resetKey: this.props.resetKey, failed: false };

  static getDerivedStateFromProps(props: RouteErrorBoundaryProps, state: RouteErrorBoundaryState): Partial<RouteErrorBoundaryState> | null {
    // 导航只清除错误；正常 URL 筛选变化不能重挂页面并丢失草稿。
    return props.resetKey === state.resetKey ? null : { resetKey: props.resetKey, failed: false };
  }

  static getDerivedStateFromError(): Partial<RouteErrorBoundaryState> {
    return { failed: true };
  }

  override render() {
    return this.state.failed
      ? <RouteRecovery onRetry={this.props.onRetry} onReload={this.props.onReload} />
      : this.props.children;
  }
}

const reloadPage = (): void => window.location.reload();

export const RouteContentBoundary = ({ children, resetKey, onReload = reloadPage }: RouteContentBoundaryProps) => {
  const [attempt, setAttempt] = useState(0);

  return <RouteErrorBoundary key={attempt} resetKey={resetKey} onRetry={() => setAttempt((current) => current + 1)} onReload={onReload}>
    {children}
  </RouteErrorBoundary>;
};

/** React 默认会记录原始异常；路由恢复只报告固定 code，避免输出正文、凭据或 URL。 */
export const reportCaughtRouteError: NonNullable<RootOptions["onCaughtError"]> = (error, { errorBoundary }) => {
  if (errorBoundary instanceof RouteErrorBoundary) {
    console.error("ROUTE_CONTENT_ERROR");
    return;
  }
  console.error(error);
};
