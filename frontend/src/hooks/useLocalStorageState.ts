import { useEffect, useState } from "react";

/**
 * Read a value from localStorage, returning null when storage is unavailable
 * (private browsing, disabled, security errors) instead of throwing.
 */
export function getStoredValue(key: string): string | null {
  try {
    return localStorage.getItem(key);
  } catch {
    return null;
  }
}

/**
 * Write a value to localStorage, swallowing errors when storage is unavailable
 * or the quota is exceeded.
 */
export function setStoredValue(key: string, value: string): void {
  try {
    localStorage.setItem(key, value);
  } catch {
    console.error(`Failed to persist "${key}" to localStorage`);
  }
}

/**
 * State persisted to localStorage as a plain string. Reads and writes are
 * guarded so disabled or unavailable storage never throws.
 *
 * `deserialize` turns the raw stored string (or null when absent) into the
 * initial value — also the place to validate and run one-off migrations.
 */
export function useLocalStorageState<T extends string>(
  key: string,
  deserialize: (raw: string | null) => T,
): [T, (value: T) => void] {
  const [value, setValue] = useState<T>(() => deserialize(getStoredValue(key)));

  useEffect(() => {
    setStoredValue(key, value);
  }, [key, value]);

  return [value, setValue];
}
