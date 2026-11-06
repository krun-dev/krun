import { api } from "./api.ts";

export async function readBuildLog(path: string, signal?: AbortSignal) {
  let pollSeconds: number | null = null;
  const query = new URLSearchParams({ inline: "true" });
  const result = await api<{ state: string; log: string }>(
    path + (path.includes("?") ? "&" : "?") + query,
    "GET",
    undefined,
    signal,
    (headers) => {
      const seconds = Number(headers.get("X-Log-Poll-Interval"));
      if (Number.isFinite(seconds) && seconds > 0 && seconds <= 3600) {
        pollSeconds = seconds;
      }
    },
  );
  return { state: result.state, log: result.log, pollSeconds };
}
