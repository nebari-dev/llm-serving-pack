import { atom } from "jotai";

import type { ApiKey } from "@/lib/types";

/**
 * Which modal dialog is currently open, plus the data it needs. Modeled as a
 * single discriminated union so only one dialog can be open at a time.
 */
export type DialogState =
  | { type: "none" }
  | { type: "create" }
  | { type: "created"; clientId: string; apiKey: string }
  | { type: "revoke"; key: ApiKey };

export const dialogAtom = atom<DialogState>({ type: "none" });
