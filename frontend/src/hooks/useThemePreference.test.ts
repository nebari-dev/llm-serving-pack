import { act, renderHook } from "@testing-library/react";
import { afterEach, describe, expect, it } from "vitest";

import { useThemePreference } from "./useThemePreference";

afterEach(() => {
  localStorage.clear();
  document.documentElement.classList.remove("dark");
});

describe("useThemePreference", () => {
  it("defaults to system mode", () => {
    const { result } = renderHook(() => useThemePreference());
    expect(result.current.themeMode).toBe("system");
  });

  it("toggles the dark class when set to dark", () => {
    const { result } = renderHook(() => useThemePreference());

    act(() => result.current.setThemeMode("dark"));
    expect(document.documentElement.classList.contains("dark")).toBe(true);

    act(() => result.current.setThemeMode("light"));
    expect(document.documentElement.classList.contains("dark")).toBe(false);
  });

  it("persists the selected mode", () => {
    const { result } = renderHook(() => useThemePreference());
    act(() => result.current.setThemeMode("dark"));
    expect(localStorage.getItem("nebari-llm:themeMode")).toBe("dark");
  });
});
