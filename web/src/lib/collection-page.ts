import { useEffect, useState } from "react";
import { api, type List } from "./api";
import { errorMessage } from "./i18n";

export function useCollectionPage<T>(
  endpoint: string,
  enabled: boolean,
  revision: number | string,
) {
  const [items, setItems] = useState<T[] | null>(null);
  const [error, setError] = useState("");
  const [loading, setLoading] = useState(false);
  const [search, setSearch] = useState("");
  const [pageSize, setPageSize] = useState(20);
  const [cursors, setCursors] = useState([""]);
  const [nextAfter, setNextAfter] = useState("");
  const after = cursors.at(-1) || "";

  useEffect(() => {
    setItems(null);
    setSearch("");
    setCursors([""]);
    setNextAfter("");
  }, [endpoint]);

  useEffect(() => {
    if (!enabled) {
      return;
    }
    const controller = new AbortController();
    setLoading(true);
    setItems(null);
    setError("");
    const query = new URLSearchParams({
      q: search,
      limit: String(pageSize),
      after,
    });
    void api<List<T>>(
      endpoint + (endpoint.includes("?") ? "&" : "?") + query,
      "GET",
      undefined,
      controller.signal,
    )
      .then((result) => {
        if (!controller.signal.aborted) {
          setItems(result.items);
          setNextAfter(result.next_after || "");
        }
      })
      .catch((cause) => {
        if (!controller.signal.aborted) {
          setError(errorMessage(cause));
        }
      })
      .finally(() => {
        if (!controller.signal.aborted) {
          setLoading(false);
        }
      });
    return () => controller.abort();
  }, [endpoint, enabled, revision, search, pageSize, after]);

  return {
    items,
    error,
    loading,
    search,
    pageSize,
    pageNumber: cursors.length,
    hasPrevious: cursors.length > 1,
    hasNext: Boolean(nextAfter),
    searchFor(value: string) {
      setSearch(value.trim());
      setCursors([""]);
    },
    resize(value: number) {
      setPageSize(value);
      setCursors([""]);
    },
    previous() {
      setCursors((current) => (current.length > 1 ? current.slice(0, -1) : current));
    },
    next() {
      if (nextAfter) {
        setCursors((current) => [...current, nextAfter]);
      }
    },
  };
}
