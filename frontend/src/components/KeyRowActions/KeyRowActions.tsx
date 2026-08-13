import { useSetAtom } from "jotai";
import { MoreVertical, Trash2 } from "lucide-react";

import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuGroup,
  DropdownMenuGroupLabel,
  DropdownMenuItem,
  DropdownMenuPortal,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import type { ApiKey } from "@/lib/types";
import { dialogAtom } from "@/store/dialogAtoms";

/** Per-row kebab menu exposing the destructive Revoke action. */
export function KeyRowActions({ apiKey }: { apiKey: ApiKey }) {
  const setDialog = useSetAtom(dialogAtom);

  return (
    <DropdownMenu modal={false}>
      <DropdownMenuTrigger variant="ghost" className="w-8 px-0" aria-label="Key actions">
        <MoreVertical className="size-4" />
      </DropdownMenuTrigger>
      <DropdownMenuPortal>
        <DropdownMenuContent align="end">
          <DropdownMenuGroup>
            <DropdownMenuGroupLabel>Danger</DropdownMenuGroupLabel>
            <DropdownMenuItem
              variant="destructive"
              onClick={() => setDialog({ type: "revoke", key: apiKey })}
            >
              <Trash2 className="size-4" /> Revoke
            </DropdownMenuItem>
          </DropdownMenuGroup>
        </DropdownMenuContent>
      </DropdownMenuPortal>
    </DropdownMenu>
  );
}
