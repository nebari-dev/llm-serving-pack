import { useSetAtom } from "jotai";
import { MoreVertical, Trash2 } from "lucide-react";

import { Button } from "@/components/ui/button";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import type { ApiKey } from "@/lib/types";
import { dialogAtom } from "@/store/dialogAtoms";

/** Per-row kebab menu exposing the destructive Revoke action. */
export function KeyRowActions({ apiKey }: { apiKey: ApiKey }) {
  const setDialog = useSetAtom(dialogAtom);

  return (
    <DropdownMenu>
      <DropdownMenuTrigger asChild>
        <Button variant="ghost" size="icon" aria-label="Key actions">
          <MoreVertical className="size-4" />
        </Button>
      </DropdownMenuTrigger>
      <DropdownMenuContent align="end">
        <DropdownMenuLabel className="text-muted-foreground text-xs">Danger</DropdownMenuLabel>
        <DropdownMenuItem
          variant="destructive"
          onSelect={() => setDialog({ type: "revoke", key: apiKey })}
        >
          <Trash2 className="size-4" /> Revoke
        </DropdownMenuItem>
      </DropdownMenuContent>
    </DropdownMenu>
  );
}
