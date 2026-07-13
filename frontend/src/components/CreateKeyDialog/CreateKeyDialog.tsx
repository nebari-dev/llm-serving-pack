import { useAtom } from "jotai";
import { type FormEvent, useState } from "react";

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
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { useCreateKey } from "@/hooks/useApiKeys";
import { useModels } from "@/hooks/useModels";
import { dialogAtom } from "@/store/dialogAtoms";

export function CreateKeyDialog() {
  const [dialog, setDialog] = useAtom(dialogAtom);
  const { data: models } = useModels();
  const createKey = useCreateKey();

  const [modelName, setModelName] = useState("");
  const [description, setDescription] = useState("");
  const [fieldError, setFieldError] = useState<string | null>(null);

  const open = dialog.type === "create";

  // Base UI's Select renders the raw value unless given a label mapping, so we
  // derive {label, value} items once and reuse them for both the options and
  // the trigger's value→label lookup.
  const modelItems = (models ?? []).map((model) => ({
    value: model.name,
    label: model.namespace ? `${model.namespace}/${model.name}` : model.name,
  }));

  function close() {
    setDialog({ type: "none" });
    setModelName("");
    setDescription("");
    setFieldError(null);
  }

  async function onSubmit(event: FormEvent) {
    event.preventDefault();
    setFieldError(null);

    if (!modelName) {
      setFieldError("Please select a model.");
      return;
    }

    try {
      const result = await createKey.mutateAsync({ modelName, description: description.trim() });
      setDialog({ type: "created", clientId: result.clientId, apiKey: result.apiKey });
      setModelName("");
      setDescription("");
    } catch (err) {
      setFieldError(
        `Failed to create key: ${err instanceof Error ? err.message : "unknown error"}`,
      );
    }
  }

  return (
    <Dialog open={open} onOpenChange={(next) => !next && close()}>
      <DialogContent className="sm:max-w-md">
        <DialogHeader>
          <DialogTitle>Create API Key</DialogTitle>
        </DialogHeader>
        <form onSubmit={onSubmit} className="flex flex-col gap-4">
          <div className="flex flex-col gap-2">
            <Label htmlFor="model-select">Model</Label>
            <Select
              items={modelItems}
              value={modelName}
              onValueChange={(value) => setModelName((value as string | null) ?? "")}
            >
              <SelectTrigger id="model-select" className="w-full">
                <SelectValue>
                  {(value) =>
                    modelItems.find((item) => item.value === value)?.label ?? "Select a model"
                  }
                </SelectValue>
              </SelectTrigger>
              <SelectContent>
                {modelItems.map((item) => (
                  <SelectItem key={item.value} value={item.value}>
                    {item.label}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </div>

          <div className="flex flex-col gap-2">
            <Label htmlFor="description-input">Description</Label>
            <Input
              id="description-input"
              value={description}
              maxLength={200}
              placeholder="e.g. Research notebook"
              onChange={(e) => setDescription(e.target.value)}
            />
          </div>

          {fieldError ? <p className="text-destructive-foreground text-sm">{fieldError}</p> : null}

          <DialogFooter>
            <Button type="button" variant="secondary" onClick={close}>
              Cancel
            </Button>
            {/* Nebari's Button defaults its rendered element to type="button", so a
                bare type="submit" prop is overridden — pass it via the render element. */}
            <Button render={<button type="submit" />} disabled={createKey.isPending}>
              {createKey.isPending ? "Creating…" : "Create"}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}
