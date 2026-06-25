import { atom } from "jotai";

/** Page-level error banner message, or null when there is nothing to show. */
export const errorAtom = atom<string | null>(null);
