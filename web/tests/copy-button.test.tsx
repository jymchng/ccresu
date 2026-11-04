import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";

import { CopyButton } from "@/components/copy-button";

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("copy button", () => {
  it("copies the value and reports success", async () => {
    const user = userEvent.setup();
    const writeText = vi.fn().mockResolvedValue(undefined);
    Object.defineProperty(navigator, "clipboard", {
      configurable: true,
      value: { writeText },
    });

    render(<CopyButton value={`ccresu version`} />);
    await user.click(screen.getByRole("button", { name: /Copy to clipboard/ }));

    expect(writeText).toHaveBeenCalledWith("ccresu version");
    expect(screen.getByText("Copied")).toBeInTheDocument();
  });

  it("reports a failure instead of pretending the copy worked", async () => {
    const user = userEvent.setup();
    Object.defineProperty(navigator, "clipboard", {
      configurable: true,
      value: undefined,
    });

    render(<CopyButton value="ccresu status" />);
    await user.click(screen.getByRole("button", { name: /Copy to clipboard/ }));

    expect(screen.getByText("Copy failed")).toBeInTheDocument();
  });

  it("uses a caller-supplied label", () => {
    render(<CopyButton value="x" label="Copy command" />);
    expect(
      screen.getByRole("button", { name: "Copy command to clipboard" }),
    ).toBeInTheDocument();
  });
});
