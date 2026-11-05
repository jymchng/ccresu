"use client";

import { useEffect, useId, useRef, useState, type ReactNode } from "react";
import { createPortal } from "react-dom";

import { cn } from "@/lib/utils";

/**
 * A disclosure button plus the panel it controls.
 *
 * This replaces `<details>/<summary>` for the phone navigation. A `<details>`
 * looks like the obvious answer and is not one:
 *
 *  - it cannot report its state to assistive technology in the terms the ARIA
 *    disclosure pattern is written in, so `aria-expanded`/`aria-controls` are
 *    missing and nothing ties the trigger to the panel it opened;
 *  - it stays open after a link is taken, so the next page arrives with a menu
 *    covering the content;
 *  - Escape does nothing, and a tap anywhere outside it does nothing either.
 *
 * The panel is deliberately **not** a modal: there is no focus trap and no
 * `aria-modal`. Nothing is being blocked, so Tab must keep moving through the
 * page; what the component does guarantee is that Escape closes the panel and
 * puts focus back on the button, so a keyboard user is never dropped at the top
 * of the document.
 *
 * Accessibility details worth stating, because they are easy to get wrong:
 *
 *  - the panel is always in the DOM and hidden with the `hidden` attribute, so
 *    `aria-controls` always resolves to a real element (a dangling reference is
 *    worse than none) and `hidden` still removes it from the accessibility tree
 *    and the tab order while closed;
 *  - `hidden` only works if nothing else sets the panel's `display`, which is
 *    why the layout classes live on an inner element and `panelClassName` is
 *    limited to positioning;
 *  - one delegated click handler on the panel covers link activation, so every
 *    link added later — a doc page, an external link — closes it without being
 *    wired up individually.
 */
export function MobileDisclosure({
  label,
  trigger,
  children,
  buttonClassName,
  panelClassName,
  innerClassName,
  backdrop = false,
}: {
  /** Base name for the button's accessible label, e.g. "navigation". */
  label: string;
  /** Visible content of the trigger button. */
  trigger: ReactNode;
  children: ReactNode;
  /** Merged over the default 44px square trigger. */
  buttonClassName?: string;
  /** Positioning only — must not set `display`, or `hidden` stops working. */
  panelClassName?: string;
  innerClassName?: string;
  /** Render a full-screen sheet behind the panel so a tap outside closes it. */
  backdrop?: boolean;
}) {
  const [open, setOpen] = useState(false);
  const panelId = useId();
  const triggerRef = useRef<HTMLButtonElement>(null);

  function close({ restoreFocus = false }: { restoreFocus?: boolean } = {}) {
    setOpen(false);
    if (restoreFocus) {
      triggerRef.current?.focus();
    }
  }

  // Escape is the only key bound: the disclosure is not a dialog, so Tab and
  // Shift+Tab must keep their normal meaning outside the panel.
  useEffect(() => {
    if (!open) {
      return;
    }

    function onKeyDown(event: KeyboardEvent) {
      if (event.key === "Escape") {
        event.stopPropagation();
        close({ restoreFocus: true });
      }
    }

    document.addEventListener("keydown", onKeyDown);
    return () => document.removeEventListener("keydown", onKeyDown);
  }, [open]);

  return (
    <>
      <button
        ref={triggerRef}
        type="button"
        aria-expanded={open}
        aria-controls={panelId}
        aria-label={`${open ? "Close" : "Open"} ${label}`}
        onClick={() => setOpen((value) => !value)}
        className={cn(
          "grid size-11 place-items-center rounded-md text-muted-foreground transition-colors hover:bg-accent hover:text-accent-foreground",
          buttonClassName,
        )}
      >
        {trigger}
      </button>

      {/*
        The backdrop is portalled to <body> on purpose. The header that renders
        this component is `backdrop-blur`, and a backdrop filter makes the
        element a containing block for `position: fixed` descendants — a fixed
        overlay inside it would be sized to the 56px header strip, not the
        viewport, and taps on the page below would land on the page instead.
        Portalling sidesteps the containing block entirely, so the sheet covers
        what the reader actually sees.
      */}
      {backdrop && open
        ? createPortal(
            <div
              aria-hidden="true"
              data-testid="disclosure-backdrop"
              onClick={() => close()}
              className="fixed inset-0 z-30 bg-foreground/20"
            />,
            document.body,
          )
        : null}

      <div id={panelId} hidden={!open} className={panelClassName}>
        <div
          className={innerClassName}
          onClick={(event) => {
            // A link is the end of the gesture: close before the navigation
            // renders, so the menu is not left open behind the new page.
            if ((event.target as HTMLElement).closest("a")) {
              close();
            }
          }}
        >
          {children}
        </div>
      </div>
    </>
  );
}
