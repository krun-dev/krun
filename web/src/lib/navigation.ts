export type Section =
  | "overview"
  | "orgs"
  | "members"
  | "groups"
  | "roles"
  | "keys"
  | "git-authorizations"
  | "check"
  | "audit"
  | "users"
  | "system-config"
  | "workers"
  | "buckets"
  | "hyperdrive";

export type OrganizationPermissions = {
  organization_id: string;
  actions: Record<string, boolean>;
  revision: number;
};

export function canOpenSection(
  section: Section,
  systemView: boolean,
  systemAdmin: boolean,
  organization: string,
  permissions: OrganizationPermissions | null,
): boolean {
  if (systemView) {
    return (
      systemAdmin &&
      ["orgs", "users", "audit", "workers", "buckets", "keys", "system-config"].includes(section)
    );
  }
  if (section === "overview") {
    return true;
  }
  if (!organization || permissions?.organization_id !== organization) {
    return false;
  }
  const allowed = (action: string) => permissions.actions[action] === true;
  switch (section) {
    case "git-authorizations":
    case "workers":
    case "hyperdrive":
      return allowed("worker.list");
    case "buckets":
      return allowed("storage.object.read");
    case "members":
      return allowed("member.read");
    case "groups":
    case "roles":
      return allowed("iam.read");
    case "keys":
      return allowed("apiKey.create");
    case "check":
      return allowed("organization.read");
    case "audit":
      return allowed("audit.read");
    default:
      return false;
  }
}
