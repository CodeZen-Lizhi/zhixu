import * as DialogPrimitive from "@radix-ui/react-dialog";
import { X } from "lucide-react";
import type { ComponentProps, ReactNode, RefObject } from "react";

type ButtonVariant = "primary" | "secondary" | "ghost" | "danger";
type ButtonSize = "sm" | "md";
export type ButtonProps = ComponentProps<"button"> & { variant?: ButtonVariant; size?: ButtonSize };
export const Button = ({ className, variant = "primary", size = "md", ...props }: ButtonProps) => (
  <button className={["ui-button", `ui-button--${variant}`, `ui-button--${size}`, className].filter(Boolean).join(" ")} {...props} />
);
export const Badge = ({ children, tone = "neutral" }: { children: ReactNode; tone?: "neutral" | "success" | "warning" | "danger" | "info" }) => <span className={`ui-badge ui-badge--${tone}`}>{children}</span>;
export const Card = ({ children, className }: { children: ReactNode; className?: string }) => <section className={["ui-card", className].filter(Boolean).join(" ")}>{children}</section>;
export const CardHeader = ({ eyebrow, title, description, action }: { eyebrow?: string; title: string; description?: string; action?: ReactNode }) => <div className="ui-card__header"><div>{eyebrow ? <p className="eyebrow">{eyebrow}</p> : null}<h2>{title}</h2>{description ? <p>{description}</p> : null}</div>{action}</div>;
export const EmptyState = ({ title, description, action }: { title: string; description: string; action?: ReactNode }) => <div className="ui-empty"><p className="eyebrow">暂无记录</p><h3>{title}</h3><p>{description}</p>{action}</div>;
export const ErrorState = ({ title = "暂时无法读取", description, onRetry }: { title?: string; description: string; onRetry?: () => void }) => <div className="ui-state ui-state--error" role="alert"><strong>{title}</strong><p>{description}</p>{onRetry ? <Button variant="secondary" onClick={onRetry}>重试</Button> : null}</div>;
export const UnavailableState = ({ title, description }: { title: string; description: string }) => <div className="ui-state ui-state--warning"><strong>{title}</strong><p>{description}</p></div>;
export const Dialog = ({ open, onOpenChange, title, description, children, restoreFocusRef }: { open: boolean; onOpenChange: (open: boolean) => void; title: string; description?: string; children: ReactNode; restoreFocusRef?: RefObject<HTMLElement | null> }) => <DialogPrimitive.Root open={open} onOpenChange={onOpenChange}><DialogPrimitive.Portal><DialogPrimitive.Overlay className="ui-dialog__overlay" /><DialogPrimitive.Content className="ui-dialog__content" onCloseAutoFocus={(event) => { if (restoreFocusRef?.current) { event.preventDefault(); restoreFocusRef.current.focus(); } }}><DialogPrimitive.Title>{title}</DialogPrimitive.Title>{description ? <DialogPrimitive.Description>{description}</DialogPrimitive.Description> : null}{children}<DialogPrimitive.Close asChild><button className="ui-dialog__close" aria-label="关闭"><X size={18} /></button></DialogPrimitive.Close></DialogPrimitive.Content></DialogPrimitive.Portal></DialogPrimitive.Root>;
