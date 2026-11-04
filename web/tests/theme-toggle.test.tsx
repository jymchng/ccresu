import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it } from "vitest";

import { ThemeToggle } from "@/components/theme-toggle";
import { applyTheme, themeStorageKey } from "@/lib/theme";

describe("theme toggle", () => {
  it("starts dark, then switches to light and remembers it", async () => {
    const user = userEvent.setup();
    document.documentElement.classList.add("dark");

    render(<ThemeToggle />);
    const button = screen.getByRole("button", { name: "Switch to light theme" });
    expect(button).toHaveAttribute("data-theme", "dark");

    await user.click(button);

    expect(document.documentElement.classList.contains("light")).toBe(true);
    expect(document.documentElement.classList.contains("dark")).toBe(false);
    expect(document.documentElement.style.colorScheme).toBe("light");
    expect(localStorage.getItem(themeStorageKey)).toBe("light");
    expect(
      screen.getByRole("button", { name: "Switch to dark theme" }),
    ).toBeInTheDocument();
  });

  it("switches back to dark", async () => {
    const user = userEvent.setup();
    applyTheme("light", document.documentElement);

    render(<ThemeToggle />);
    await user.click(screen.getByRole("button", { name: "Switch to dark theme" }));

    expect(document.documentElement.classList.contains("dark")).toBe(true);
    expect(localStorage.getItem(themeStorageKey)).toBe("dark");
  });

  it("keeps working when localStorage throws", async () => {
    const user = userEvent.setup();
    const original = Storage.prototype.setItem;
    Storage.prototype.setItem = () => {
      throw new Error("blocked");
    };

    try {
      render(<ThemeToggle />);
      await user.click(screen.getByRole("button", { name: /Switch to/ }));
      expect(document.documentElement.classList.contains("light")).toBe(true);
    } finally {
      Storage.prototype.setItem = original;
    }
  });
});
