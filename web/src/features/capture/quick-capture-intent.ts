export const quickCaptureIntentEvent = "zhixu:quick-capture";

/** 由 AppShell 持有弹窗，业务入口只发送一次同窗口 UI intent。 */
export const requestQuickCapture = (): void => {
  window.dispatchEvent(new Event(quickCaptureIntentEvent));
};
