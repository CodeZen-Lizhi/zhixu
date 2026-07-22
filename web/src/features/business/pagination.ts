import { useCallback, useState } from "react";

interface ScopedCursorState {
  scopeKey: string;
  cursor: string;
}

// Opaque server cursors stay in local state and are discarded when any effective query input changes.
export const useScopedCursor = (scope: readonly string[]): readonly [string, (cursor: string) => void] => {
  const scopeKey = JSON.stringify(scope);
  const [state, setState] = useState<ScopedCursorState>({ scopeKey, cursor: "" });

  // Derive the first page during the scope-changing render, then commit that reset so A -> B -> A cannot revive A's cursor.
  if (state.scopeKey !== scopeKey) setState({ scopeKey, cursor: "" });

  const cursor = state.scopeKey === scopeKey ? state.cursor : "";
  const setCursor = useCallback((nextCursor: string) => {
    setState({ scopeKey, cursor: nextCursor });
  }, [scopeKey]);
  return [cursor, setCursor];
};
