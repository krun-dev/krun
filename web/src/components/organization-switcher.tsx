import * as DropdownMenu from "radix-ui/dropdown-menu";
import { Building2, Check, ChevronsUpDown } from "lucide-react";
import { useTranslation } from "react-i18next";
import type { Organization } from "@/lib/api";
import { t } from "@/lib/i18n";

export function OrganizationSwitcher({
  organizations,
  value,
  onChange,
}: {
  organizations: Pick<Organization, "id" | "name">[];
  value: string;
  onChange: (id: string) => void;
}) {
  useTranslation();
  const organization = organizations.find((item) => item.id === value);
  const name = organization?.name ?? t("选择组织");

  return (
    <DropdownMenu.Root>
      <DropdownMenu.Trigger asChild>
        <button
          type="button"
          aria-label={`${t("切换组织")}: ${name}`}
          title={name}
          disabled={organizations.length === 0}
          className="flex w-full min-w-0 items-center gap-2.5 rounded-md px-2 py-2.5 text-left transition-colors hover:bg-secondary focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring disabled:cursor-default disabled:opacity-50 data-[state=open]:bg-secondary"
        >
          <span className="flex size-8 shrink-0 items-center justify-center rounded-md border bg-background">
            <Building2
              size={16}
              aria-hidden="true"
            />
          </span>
          <span className="min-w-0 flex-1 truncate text-sm font-medium">{name}</span>
          <ChevronsUpDown
            size={14}
            className="shrink-0 text-muted-foreground"
            aria-hidden="true"
          />
        </button>
      </DropdownMenu.Trigger>
      <DropdownMenu.Portal>
        <DropdownMenu.Content
          align="start"
          sideOffset={6}
          collisionPadding={12}
          className="z-50 max-h-[var(--radix-dropdown-menu-content-available-height)] w-[var(--radix-dropdown-menu-trigger-width)] overflow-y-auto rounded-md border bg-popover p-1 text-popover-foreground shadow-md"
        >
          <DropdownMenu.Label className="px-2 py-1.5 text-xs text-muted-foreground">
            {t("切换组织")}
          </DropdownMenu.Label>
          <DropdownMenu.RadioGroup value={value}>
            {organizations.map((item) => (
              <DropdownMenu.RadioItem
                key={item.id}
                value={item.id}
                onSelect={() => onChange(item.id)}
                className="flex min-h-10 cursor-default items-center gap-2 rounded-sm px-2 py-2 text-sm outline-none select-none data-[highlighted]:bg-accent data-[highlighted]:text-accent-foreground"
              >
                <span className="min-w-0 flex-1 break-words">{item.name}</span>
                <span className="flex size-4 shrink-0 items-center justify-center">
                  <DropdownMenu.ItemIndicator>
                    <Check
                      size={14}
                      aria-hidden="true"
                    />
                  </DropdownMenu.ItemIndicator>
                </span>
              </DropdownMenu.RadioItem>
            ))}
          </DropdownMenu.RadioGroup>
        </DropdownMenu.Content>
      </DropdownMenu.Portal>
    </DropdownMenu.Root>
  );
}
