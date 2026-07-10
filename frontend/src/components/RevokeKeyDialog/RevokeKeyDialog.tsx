import { useAtom, useSetAtom } from "jotai";
import { Trash2 } from "lucide-react";

import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { useRevokeKey } from "@/hooks/useApiKeys";
import { errorAtom } from "@/store/appAtoms";
import { dialogAtom } from "@/store/dialogAtoms";

export function RevokeKeyDialog() {
  const [dialog, setDialog] = useAtom(dialogAtom);
  const setError = useSetAtom(errorAtom);
  const revokeKey = useRevokeKey();

  const open = dialog.type === "revoke";
  const key = dialog.type === "revoke" ? dialog.key : null;
  const label = key?.description || key?.clientId || "";

  function close() {
    setDialog({ type: "none" });
  }

  async function confirm() {
    if (!key) {
      return;
    }
    try {
      await revokeKey.mutateAsync(key);
      close();
    } catch (err) {
      close();
      setError(`Failed to revoke key: ${err instanceof Error ? err.message : "unknown error"}`);
    }
  }

  return (
    <Dialog open={open} onOpenChange={(next) => !next && close()}>
      <DialogContent className="sm:max-w-lg">
        <DialogHeader>
          <DialogTitle>Revoke API Key?</DialogTitle>
          <DialogDescription>
            This permanently disables "{label}". Any application using this key will immediately
            lose access. This can't be undone.
          </DialogDescription>
        </DialogHeader>
        <DialogFooter>
          <Button type="button" variant="secondary" onClick={close}>
            Cancel
          </Button>
          <Button
            type="button"
            variant="destructive"
            onClick={confirm}
            disabled={revokeKey.isPending}
          >
            <Trash2 className="size-4" /> {revokeKey.isPending ? "Revoking…" : "Revoke Key"}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
