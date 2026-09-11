import { screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";

import { THEME_STORAGE_KEY } from "@/app/theme";
import { signOut } from "@/auth/keycloak";
import { renderWithProviders } from "@/test/render";

import { Topbar } from "./Topbar";

// Keep the real initKeycloak/getKeycloakInstance (the test setup's fake
// session drives useUser), but stub signOut so clicking the menu item does not
// try a real logout redirect.
vi.mock("@/auth/keycloak", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@/auth/keycloak")>();
  return { ...actual, signOut: vi.fn() };
});

afterEach(() => {
  vi.clearAllMocks();
  localStorage.clear();
  document.documentElement.classList.remove("dark");
});

describe("Topbar", () => {
  it("renders the brand logo linking home", () => {
    renderWithProviders(<Topbar />);

    const brand = screen.getByRole("link", { name: /go to homepage/i });
    expect(brand).toHaveAttribute("href", "/");
  });

  it("shows the signed-in user's name", () => {
    renderWithProviders(<Topbar />);

    expect(screen.getByText("Test User")).toBeInTheDocument();
  });

  it("selects a theme mode from the profile menu", async () => {
    const user = userEvent.setup();
    renderWithProviders(<Topbar />);

    await user.click(screen.getByRole("button", { name: /account menu/i }));
    await user.click(await screen.findByRole("menuitemradio", { name: /dark mode/i }));

    expect(localStorage.getItem(THEME_STORAGE_KEY)).toBe("dark");
    expect(document.documentElement.classList.contains("dark")).toBe(true);

    await user.click(screen.getByRole("menuitemradio", { name: /light mode/i }));
    expect(localStorage.getItem(THEME_STORAGE_KEY)).toBe("light");
    expect(document.documentElement.classList.contains("dark")).toBe(false);
  });

  it("reflects the current theme mode via aria-checked", async () => {
    localStorage.setItem(THEME_STORAGE_KEY, "dark");
    const user = userEvent.setup();
    renderWithProviders(<Topbar />);

    await user.click(screen.getByRole("button", { name: /account menu/i }));

    expect(await screen.findByRole("menuitemradio", { name: /dark mode/i })).toHaveAttribute(
      "aria-checked",
      "true",
    );
    expect(screen.getByRole("menuitemradio", { name: /light mode/i })).toHaveAttribute(
      "aria-checked",
      "false",
    );
    expect(screen.getByRole("menuitemradio", { name: /system theme/i })).toHaveAttribute(
      "aria-checked",
      "false",
    );
  });

  it("calls signOut from the account menu", async () => {
    const user = userEvent.setup();
    renderWithProviders(<Topbar />);

    await user.click(screen.getByRole("button", { name: /account menu/i }));
    await user.click(await screen.findByRole("menuitem", { name: /sign out/i }));

    expect(signOut).toHaveBeenCalledOnce();
  });
});
