import { useSetAtom } from "jotai";
import { Plus } from "lucide-react";

import { KeyRowActions } from "@/components/KeyRowActions";
import { Button } from "@/components/ui/button";
import { Card, CardAction, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import { useApiKeys } from "@/hooks/useApiKeys";
import { formatDate } from "@/lib/format";
import { cn } from "@/lib/utils";
import { dialogAtom } from "@/store/dialogAtoms";

export function KeysCard({ className }: { className?: string }) {
  const setDialog = useSetAtom(dialogAtom);
  const { data: keys, isLoading, isError, error } = useApiKeys();

  return (
    <Card className={cn(className)}>
      <CardHeader>
        <CardTitle>My API Keys</CardTitle>
        <CardAction>
          <Button onClick={() => setDialog({ type: "create" })}>
            <Plus className="size-4" /> Create new key
          </Button>
        </CardAction>
      </CardHeader>
      <CardContent>
        {isLoading ? (
          <p className="py-6 text-muted-foreground text-sm">Loading keys…</p>
        ) : isError ? (
          <p className="py-6 text-destructive text-sm">
            Failed to load keys: {error instanceof Error ? error.message : "unknown error"}
          </p>
        ) : !keys || keys.length === 0 ? (
          <p className="py-6 text-muted-foreground text-sm">No API keys yet.</p>
        ) : (
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>Name / Description</TableHead>
                <TableHead>Client ID</TableHead>
                <TableHead>Model</TableHead>
                <TableHead>Created</TableHead>
                <TableHead className="text-right">Action</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {keys.map((key) => (
                <TableRow key={`${key.namespace}/${key.modelName}/${key.clientId}`}>
                  <TableCell>{key.description || "—"}</TableCell>
                  <TableCell className="font-mono text-xs">{key.clientId}</TableCell>
                  <TableCell>{key.modelName}</TableCell>
                  <TableCell className="text-muted-foreground">
                    {formatDate(key.createdAt)}
                  </TableCell>
                  <TableCell className="text-right">
                    <KeyRowActions apiKey={key} />
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        )}
      </CardContent>
    </Card>
  );
}
