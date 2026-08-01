/* eslint-disable @typescript-eslint/array-type */
import * as DialogPrimitive from "@radix-ui/react-dialog";
import * as DropdownMenuPrimitive from "@radix-ui/react-dropdown-menu";
import { Slot } from "@radix-ui/react-slot";
import * as TabsPrimitive from "@radix-ui/react-tabs";
import * as TooltipPrimitive from "@radix-ui/react-tooltip";
import { cva, type VariantProps } from "class-variance-authority";
import { X } from "lucide-react";
import type { ComponentProps, ReactElement, ReactNode, RefObject } from "react";
import { clsx } from "clsx";

export const cn = (...values: Array<string | false | null | undefined>) => clsx(values);
const buttonVariants = cva("ui-button", { variants: { variant: { primary: "ui-button--primary", secondary: "ui-button--secondary", ghost: "ui-button--ghost", danger: "ui-button--danger" }, size: { sm: "ui-button--sm", md: "ui-button--md" } }, defaultVariants: { variant: "primary", size: "md" } });
export type ButtonProps = ComponentProps<"button"> & VariantProps<typeof buttonVariants> & { asChild?: boolean };
export const Button = ({ className, variant, size, asChild = false, ...props }: ButtonProps) => { const Comp = asChild ? Slot : "button"; return <Comp className={cn(buttonVariants({ variant, size }), className)} {...props} />; };
export const Badge = ({ children, tone = "neutral" }: { children: ReactNode; tone?: "neutral" | "success" | "warning" | "danger" | "info" }) => <span className={`ui-badge ui-badge--${tone}`}>{children}</span>;
export const Card = ({ children, className }: { children: ReactNode; className?: string }) => <section className={cn("ui-card", className)}>{children}</section>;
export const CardHeader = ({ eyebrow, title, description, action }: { eyebrow?: string; title: string; description?: string; action?: ReactNode }) => <div className="ui-card__header"><div>{eyebrow ? <p className="eyebrow">{eyebrow}</p> : null}<h2>{title}</h2>{description ? <p>{description}</p> : null}</div>{action}</div>;
export const PageHeader = ({ title, description, action }: { title: string; description?: string; action?: ReactNode }) => <header className="page-header"><div><h1>{title}</h1>{description ? <p>{description}</p> : null}</div>{action ? <div className="page-header__actions">{action}</div> : null}</header>;
export const EmptyState = ({ title, description, action }: { title: string; description: string; action?: ReactNode }) => <div className="ui-empty"><h3>{title}</h3><p>{description}</p>{action}</div>;
export const ErrorState = ({ title = "暂时无法读取", description, onRetry }: { title?: string; description: string; onRetry?: () => void }) => <div className="ui-state ui-state--error" role="alert"><strong>{title}</strong><p>{description}</p>{onRetry ? <Button variant="secondary" onClick={onRetry}>重试</Button> : null}</div>;
export const UnavailableState = ({ title, description }: { title: string; description: string }) => <div className="ui-state ui-state--unavailable"><strong>{title}</strong><p>{description}</p></div>;
export const Dialog = ({ open, onOpenChange, title, description, children, restoreFocusRef }: { open: boolean; onOpenChange: (open: boolean) => void; title: string; description?: string; children: ReactNode; restoreFocusRef?: RefObject<HTMLElement | null> }) => <DialogPrimitive.Root open={open} onOpenChange={onOpenChange}><DialogPrimitive.Portal><DialogPrimitive.Overlay className="ui-dialog__overlay" /><DialogPrimitive.Content className="ui-dialog__content" onCloseAutoFocus={(event) => { if (restoreFocusRef?.current) { event.preventDefault(); restoreFocusRef.current.focus(); } }}><DialogPrimitive.Title>{title}</DialogPrimitive.Title>{description ? <DialogPrimitive.Description>{description}</DialogPrimitive.Description> : null}{children}<DialogPrimitive.Close asChild><button className="ui-dialog__close" aria-label="关闭"><X size={18} /></button></DialogPrimitive.Close></DialogPrimitive.Content></DialogPrimitive.Portal></DialogPrimitive.Root>;
export const Sheet = ({ open, onOpenChange, title, children, restoreFocusRef }: { open: boolean; onOpenChange: (open: boolean) => void; title: string; children: ReactNode; restoreFocusRef?: RefObject<HTMLElement | null> }) => <DialogPrimitive.Root open={open} onOpenChange={onOpenChange}><DialogPrimitive.Portal><DialogPrimitive.Overlay className="ui-dialog__overlay" /><DialogPrimitive.Content className="ui-sheet__content" onCloseAutoFocus={(event) => { event.preventDefault(); restoreFocusRef?.current?.focus(); }}><DialogPrimitive.Title className="ui-sheet__title">{title}</DialogPrimitive.Title>{children}<DialogPrimitive.Close asChild><button className="ui-dialog__close" aria-label="关闭导航"><X size={18} /></button></DialogPrimitive.Close></DialogPrimitive.Content></DialogPrimitive.Portal></DialogPrimitive.Root>;

export const Tabs = TabsPrimitive.Root;
export const TabsList = ({ className, ...props }: ComponentProps<typeof TabsPrimitive.List>) => <TabsPrimitive.List className={cn("ui-tabs__list", className)} {...props} />;
export const TabsTrigger = ({ className, ...props }: ComponentProps<typeof TabsPrimitive.Trigger>) => <TabsPrimitive.Trigger className={cn("ui-tabs__trigger", className)} {...props} />;
export const TabsContent = ({ className, ...props }: ComponentProps<typeof TabsPrimitive.Content>) => <TabsPrimitive.Content className={cn("ui-tabs__content", className)} {...props} />;

export const Tooltip = ({ children, content }: { children: ReactElement; content: ReactNode }) => <TooltipPrimitive.Provider delayDuration={250}><TooltipPrimitive.Root><TooltipPrimitive.Trigger asChild>{children}</TooltipPrimitive.Trigger><TooltipPrimitive.Portal><TooltipPrimitive.Content className="ui-tooltip" sideOffset={8}>{content}<TooltipPrimitive.Arrow className="ui-tooltip__arrow" /></TooltipPrimitive.Content></TooltipPrimitive.Portal></TooltipPrimitive.Root></TooltipPrimitive.Provider>;

export const DropdownMenu = ({ trigger, label, children, align = "end", side = "bottom" }: { trigger: ReactElement; label: string; children: ReactNode; align?: "start" | "center" | "end"; side?: "top" | "right" | "bottom" | "left" }) => <DropdownMenuPrimitive.Root><DropdownMenuPrimitive.Trigger asChild>{trigger}</DropdownMenuPrimitive.Trigger><DropdownMenuPrimitive.Portal><DropdownMenuPrimitive.Content className="ui-dropdown" align={align} side={side} sideOffset={8} aria-label={label}>{children}</DropdownMenuPrimitive.Content></DropdownMenuPrimitive.Portal></DropdownMenuPrimitive.Root>;
export const DropdownMenuLabel = ({ children }: { children: ReactNode }) => <DropdownMenuPrimitive.Label className="ui-dropdown__label">{children}</DropdownMenuPrimitive.Label>;
export const DropdownMenuSeparator = () => <DropdownMenuPrimitive.Separator className="ui-dropdown__separator" />;
export const DropdownMenuItem = ({ className, ...props }: ComponentProps<typeof DropdownMenuPrimitive.Item>) => <DropdownMenuPrimitive.Item className={cn("ui-dropdown__item", className)} {...props} />;
