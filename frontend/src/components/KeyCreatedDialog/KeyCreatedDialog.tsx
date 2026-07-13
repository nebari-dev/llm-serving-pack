import { useAtom } from "jotai";
import { AlertTriangle, Check, Copy, Download } from "lucide-react";
import { useState } from "react";

import { Alert, AlertDescription } from "@/components/ui/alert";
import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { dialogAtom } from "@/store/dialogAtoms";

export function KeyCreatedDialog() {
  const [dialog, setDialog] = useAtom(dialogAtom);
  const [copied, setCopied] = useState(false);

  const open = dialog.type === "created";
  const clientId = dialog.type === "created" ? dialog.clientId : "";
  const apiKey = dialog.type === "created" ? dialog.apiKey : "";

  function close() {
    setDialog({ type: "none" });
    setCopied(false);
  }

  async function copyKey() {
    try {
      await navigator.clipboard.writeText(apiKey);
      setCopied(true);
      setTimeout(() => setCopied(false), 2000);
    } catch {
      // Clipboard can be unavailable (insecure context); the field stays
      // selectable so the user can copy manually.
    }
  }

  function download() {
    const contents = `Client ID: ${clientId}\nAPI Key: ${apiKey}\n`;
    const blob = new Blob([contents], { type: "text/plain" });
    const url = URL.createObjectURL(blob);
    const a = document.createElement("a");
    a.href = url;
    a.download = `${clientId || "api-key"}.txt`;
    document.body.appendChild(a);
    a.click();
    a.remove();
    URL.revokeObjectURL(url);
  }

  return (
    <Dialog open={open} onOpenChange={(next) => !next && close()}>
      <DialogContent className="sm:max-w-lg">
        <DialogHeader>
          <DialogTitle>API Key Created</DialogTitle>
        </DialogHeader>

        <div className="flex flex-col gap-4">
          <div className="flex flex-col gap-2">
            <Label htmlFor="created-client-id">Client ID</Label>
            <Input id="created-client-id" value={clientId} readOnly className="font-mono text-xs" />
          </div>

          <div className="flex flex-col gap-2">
            <Label htmlFor="created-api-key">API Key</Label>
            <div className="flex gap-2">
              <Input
                id="created-api-key"
                value={apiKey}
                readOnly
                className="font-mono text-xs"
                onFocus={(e) => e.currentTarget.select()}
              />
              <Button type="button" variant="secondary" onClick={copyKey}>
                {copied ? <Check className="size-4" /> : <Copy className="size-4" />}
                {copied ? "Copied!" : "Copy"}
              </Button>
            </div>
          </div>

          <Alert variant="destructive" className="flex items-start gap-2">
            <AlertTriangle className="size-4" />
            <AlertDescription>Copy this key now. It will not be shown again.</AlertDescription>
          </Alert>

          <p className="text-muted-foreground text-sm">
            For security reasons, this key is only displayed once and cannot be retrieved later. If
            you lose it, you'll need to create a new one.
          </p>
        </div>

        <DialogFooter>
          <Button type="button" variant="secondary" onClick={download}>
            <Download className="size-4" /> Download .txt
          </Button>
          <Button type="button" onClick={close}>
            <Check className="size-4" /> I've copied my key
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
