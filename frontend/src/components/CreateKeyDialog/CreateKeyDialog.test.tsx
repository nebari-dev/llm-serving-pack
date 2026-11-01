import { QueryClient } from "@tanstack/react-query";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { createStore } from "jotai";
import { afterEach, describe, expect, it, vi } from "vitest";

import { dialogAtom } from "@/store/dialogAtoms";
import { TestProviders } from "@/test/render";
import { CreateKeyDialog } from "./CreateKeyDialog";

const models = [
  { Name: "llama-3", Namespace: "team-a" },
  { Name: "mistral", Namespace: "" },
];

/** Stub fetch for the two endpoints the dialog touches: model list + create. */
function stubFetch() {
  const fetchMock = vi.fn(async (url: string, opts?: RequestInit) => {
    const method = opts?.method ?? "GET";
    if (url.includes("/api/models")) {
      return {
        ok: true,
        status: 200,
        json: async () => ({ models }),
        text: async () => "",
      } as Response;
    }
    if (url.includes("/api/keys") && method === "POST") {
      return {
        ok: true,
        status: 200,
        json: async () => ({ clientId: "cid-1", apiKey: "sk-test" }),
        text: async () => "",
      } as Response;
    }
    return { ok: false, status: 404, json: async () => ({}), text: async () => "" } as Response;
  });
  vi.stubGlobal("fetch", fetchMock);
  return fetchMock;
}

/** Render the dialog already open (dialogAtom = create) inside app providers. */
function renderOpen() {
  const store = createStore();
  store.set(dialogAtom, { type: "create" });
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });

  return render(<CreateKeyDialog />, {
    wrapper: ({ children }) => (
      <TestProviders queryClient={queryClient} store={store}>
        {children}
      </TestProviders>
    ),
  });
}

afterEach(() => {
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});

describe("CreateKeyDialog (Nebari Base UI Select)", () => {
  it("opens the model select, lists namespaced models, and reflects the choice", async () => {
    stubFetch();
    const user = userEvent.setup();
    renderOpen();

    expect(screen.getByText("Create API Key")).toBeInTheDocument();

    const trigger = screen.getByRole("combobox", { name: "Model" });
    expect(trigger).toHaveTextContent("Select a model");

    await user.click(trigger);
    const listbox = await screen.findByRole("listbox");
    // Labels are the {namespace}/{name} form derived in the dialog.
    await within(listbox).findByText("team-a/llama-3");
    expect(within(listbox).getByText("mistral")).toBeInTheDocument();

    await user.click(within(listbox).getByText("team-a/llama-3"));
    await waitFor(() => expect(trigger).toHaveTextContent("team-a/llama-3"));
  });

  it("submits the selected model's value (not its label) to the create endpoint", async () => {
    const fetchMock = stubFetch();
    const user = userEvent.setup();
    renderOpen();

    await user.click(await screen.findByRole("combobox", { name: "Model" }));
    const listbox = await screen.findByRole("listbox");
    await user.click(await within(listbox).findByText("team-a/llama-3"));

    await user.click(screen.getByRole("button", { name: "Create" }));

    await waitFor(() => {
      const post = fetchMock.mock.calls.find(
        ([url, opts]) => String(url).includes("/api/keys") && opts?.method === "POST",
      );
      expect(post).toBeDefined();
      expect(JSON.parse(String(post?.[1]?.body))).toMatchObject({ modelName: "llama-3" });
    });
  });
});
