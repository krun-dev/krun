import { api } from "./api";

export async function readBuildLog(path: string, signal?: AbortSignal) {
  let pollSeconds: number | null = null;
  const result = await api<{ state: string; download_url: string }>(
    path,
    "GET",
    undefined,
    signal,
    (headers) => {
      const seconds = Number(headers.get("X-Log-Poll-Interval"));
      if (Number.isFinite(seconds) && seconds > 0 && seconds <= 3600) pollSeconds = seconds;
    },
  );
  let log = "";
  if (result.download_url) {
    const response = await fetch(result.download_url, {
      credentials: "omit",
      referrerPolicy: "no-referrer",
      cache: "no-store",
      signal: signal
        ? AbortSignal.any([signal, AbortSignal.timeout(20000)])
        : AbortSignal.timeout(20000),
    });
    if (!response.ok) throw new Error("Unable to read build log from object storage");
    log = await response.text();
  }
  return { state: result.state, log, pollSeconds };
}
