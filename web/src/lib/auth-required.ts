export const AUTH_REQUIRED_EVENT = "krun:auth-required";

export function notifyAuthRequired(status: number): void {
  if (status !== 401 || typeof window === "undefined") return;
  window.dispatchEvent(new Event(AUTH_REQUIRED_EVENT));
}
