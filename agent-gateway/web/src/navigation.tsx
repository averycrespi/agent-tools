import { useLayoutEffect, useRef } from "preact/hooks";

export interface NavigationGuard {
  setDirty: (owner: symbol, dirty: boolean) => void;
  navigate: (fragment: string, discard?: boolean) => void;
}

let activeGuard: NavigationGuard | undefined;

export function configureNavigationGuard(guard: NavigationGuard): void {
  activeGuard = guard;
}

export function useUnsavedChanges(dirty: boolean): NavigationGuard["navigate"] {
  const owner = useRef(Symbol("unsaved-changes"));
  if (activeGuard === undefined)
    throw new Error("navigation guard is unavailable");
  const guard = activeGuard;
  useLayoutEffect(() => {
    guard.setDirty(owner.current, dirty);
    return () => guard.setDirty(owner.current, false);
  }, [dirty, guard]);
  return guard.navigate;
}
