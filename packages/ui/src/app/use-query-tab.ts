import { useCallback } from "react";
import { useSearchParams } from "react-router-dom";

export function useQueryTab<T extends string>(validTabs: readonly T[], fallback: T) {
  const [searchParams, setSearchParams] = useSearchParams();
  const queryTab = searchParams.get("tab");
  const tab = validTabs.includes(queryTab as T) ? queryTab as T : fallback;

  const setTab = useCallback((nextTab: T) => {
    setSearchParams((current) => {
      const nextParams = new URLSearchParams(current);
      if (nextTab === fallback) nextParams.delete("tab");
      else nextParams.set("tab", nextTab);
      return nextParams;
    }, { replace: true });
  }, [fallback, setSearchParams]);

  return [tab, setTab] as const;
}
