import { useAtom } from "jotai";
import { AlertTriangle, X } from "lucide-react";

import { Alert, AlertDescription } from "@/components/ui/alert";
import { Button } from "@/components/ui/button";
import { errorAtom } from "@/store/appAtoms";

/** Dismissible page-level error banner driven by `errorAtom`. */
export function ErrorBanner() {
  const [error, setError] = useAtom(errorAtom);

  if (!error) {
    return null;
  }

  return (
    <Alert variant="destructive" role="alert" className="flex items-start gap-2">
      <AlertTriangle className="size-4" />
      <AlertDescription className="flex-1">{error}</AlertDescription>
      <Button
        variant="ghost"
        size="icon"
        className="size-6"
        aria-label="Dismiss"
        onClick={() => setError(null)}
      >
        <X className="size-4" />
      </Button>
    </Alert>
  );
}
