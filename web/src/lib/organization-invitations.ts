export type OrganizationInvitation = {
  id: string;
  organization_id: string;
  role_id: "member" | "viewer";
  target_user_id?: string;
  target_email?: string;
  max_uses: number;
  use_count: number;
  created_at: string;
  expires_at: string;
  revoked_at: string | null;
  status: "active" | "expired" | "revoked" | "exhausted";
};

export type InvitationPreview = {
  organization_name: string;
  role_id: "member" | "viewer";
  expires_at: string;
  targeted: boolean;
};

export type InvitationResult = {
  organization_id: string;
  status: "joined" | "already_member";
  can_enter: boolean;
};

const storageKey = "krun.organization-invitation";
const tokenPattern = /^krun_invite_[A-Za-z0-9_-]{43}$/;

// Keep the capability out of return_to, OAuth state and the address bar.
// sessionStorage survives the same-tab identity-provider round trip.
export function captureOrganizationInvitation(): {
  token: string;
  error: "storage" | "missing" | null;
} {
  const url = new URL(window.location.href);
  const supplied = new URLSearchParams(url.hash.slice(1)).get("invite");
  try {
    if (supplied !== null) {
      if (!tokenPattern.test(supplied)) {
        sessionStorage.removeItem(storageKey);
        url.hash = "";
        url.searchParams.set("page", "join");
        history.replaceState(null, "", url);
        return { token: "", error: "missing" };
      }
      sessionStorage.setItem(storageKey, JSON.stringify({ token: supplied, saved_at: Date.now() }));
      url.hash = "";
      url.searchParams.set("page", "join");
      history.replaceState(null, "", url);
      return { token: supplied, error: null };
    }
    let saved;
    try {
      saved = JSON.parse(sessionStorage.getItem(storageKey) || "null");
    } catch {
      sessionStorage.removeItem(storageKey);
      return { token: "", error: "missing" };
    }
    if (
      saved &&
      tokenPattern.test(saved.token) &&
      Number.isFinite(saved.saved_at) &&
      saved.saved_at <= Date.now() &&
      Date.now() - saved.saved_at < 7 * 24 * 60 * 60 * 1000
    ) {
      return { token: saved.token, error: null };
    }
    sessionStorage.removeItem(storageKey);
    return { token: "", error: "missing" };
  } catch {
    return { token: "", error: "storage" };
  }
}

export function clearOrganizationInvitation(): void {
  try {
    sessionStorage.removeItem(storageKey);
  } catch {
    // A blocked store was already reported when capturing the invitation.
  }
}
