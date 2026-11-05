import { existsSync, readFileSync } from "node:fs";
import path from "node:path";

import { render, screen, within } from "@testing-library/react";
import { describe, expect, it } from "vitest";

import TermsPage from "@/app/terms/page";
import { termsPage } from "@/lib/static-pages";

/**
 * The liability limitation, asserted as a list of things a reader must be able
 * to find on `/terms/` — not as "a section exists".
 *
 * The MIT licence already says the authors are not liable; the point of this
 * page is to say the same thing in language a user can act on, and to name the
 * specific losses an autonomous resume can cause (quota burned after a reset,
 * a session resumed twice, a database that no longer opens). Each `it` below is
 * one clause of that promise, checked against the rendered page so the words
 * cannot be dropped from the content module without a failure.
 *
 * Where a claim is about the licence, the assertion reads `LICENSE` itself: a
 * page that paraphrases the licence incorrectly is the one bug that matters
 * here, and comparing two copies of the same string would not catch it.
 */

const repoRoot = path.resolve(import.meta.dirname, "..", "..");
const license = readFileSync(path.join(repoRoot, "LICENSE"), "utf8").replace(/\s+/g, " ");

/** The rendered `/terms/` page's "No liability" section. */
function liabilitySection(): HTMLElement {
  render(<TermsPage />);
  const heading = screen.getByRole("heading", { level: 2, name: "No liability" });
  const section = heading.closest("section");
  expect(section, "the No liability heading is not inside a section").not.toBeNull();
  return section as HTMLElement;
}

describe("Terms of Service: no liability", () => {
  it("has an explicit no-liability section on the page", () => {
    const section = liabilitySection();

    // The page opens with a plain-language sentence, then the formal exclusion.
    expect(
      within(section).getByText(/that cost is yours, not the maintainer's/i),
    ).toBeInTheDocument();
    expect(
      within(section).getByText(/is not liable for any damages of any kind/i),
    ).toBeInTheDocument();
    expect(within(section).getByText(/provided as-is/i)).toBeInTheDocument();

    // Not flannel: the section is reachable as a linkable anchor too.
    expect(termsPage.sections.some((candidate) => candidate.id === "no-liability")).toBe(true);
  });

  it("covers use and inability to use, in contract, tort or otherwise", () => {
    const section = liabilitySection();

    expect(
      within(section).getByText(/arising from the use of, or the inability to use, this software/i),
    ).toBeInTheDocument();
    expect(
      within(section).getByText(/in contract, tort or otherwise/i),
    ).toBeInTheDocument();
    // The scope is explicit rather than implied by a "for example" list.
    expect(
      within(section).getByText(/from the inability to use it/i),
    ).toBeInTheDocument();
  });

  it("names the specific losses: shifts in work, quota, database and autonomy", () => {
    const section = liabilitySection();

    for (const damage of [
      /lost work/i, // work lost when a resumed conversation does not continue
      /session resumption that is missed, delayed, attempted more than once/i,
      /usage-limit overruns/i, // quota burned after the reset, or billed later
      /data in the local SQLite database/i,
      /autonomous resume does on your behalf/i, // the daemon acting while you sleep
      /generated systemd unit/i, // the deploy path, not only the CLI
    ]) {
      expect(within(section).getByText(damage)).toBeInTheDocument();
    }
  });

  it("makes supervising resumption and third-party terms the user's job", () => {
    render(<TermsPage />);

    const responsibility = screen
      .getByRole("heading", { level: 2, name: "Your responsibility" })
      .closest("section");
    expect(responsibility).not.toBeNull();
    const body = responsibility as HTMLElement;

    expect(
      within(body).getByText(/responsible for supervising resumption/i),
    ).toBeInTheDocument();
    // The third party is named, and so is the policy that actually binds a user.
    expect(within(body).getByText(/Anthropic's own terms and usage policy/i)).toBeInTheDocument();
    expect(within(body).getByText(/does not attempt to evade any limit or policy/i)).toBeInTheDocument();
  });

  it("states that using the software is the acceptance and cannot be withdrawn", () => {
    render(<TermsPage />);

    const acceptance = screen
      .getByRole("heading", { level: 2, name: "Acceptance" })
      .closest("section");
    expect(acceptance).not.toBeNull();
    const body = acceptance as HTMLElement;

    expect(within(body).getByText(/Using the software is the acceptance/i)).toBeInTheDocument();
    expect(
      within(body).getByText(/cannot withdraw the disclaimer after the fact/i),
    ).toBeInTheDocument();
    // Use constitutes acceptance of both the page and the licence it restates.
    expect(within(body).getByText(/agree to these terms and to the MIT licence/i)).toBeInTheDocument();
  });

  it("restates the licence's own exclusion instead of contradicting it", () => {
    // The clause in LICENSE, exactly as it is written there:
    expect(license).toContain("THE SOFTWARE IS PROVIDED \"AS IS\", WITHOUT WARRANTY OF ANY KIND");
    expect(license).toContain("IN NO EVENT SHALL THE AUTHORS OR COPYRIGHT HOLDERS BE LIABLE");
    expect(license).toContain(
      "FOR ANY CLAIM, DAMAGES OR OTHER LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM, OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE SOFTWARE",
    );

    const text = [
      termsPage.title,
      termsPage.summary,
      ...termsPage.sections.flatMap((section) => [
        section.heading,
        ...(section.paragraphs ?? []),
        ...(section.bullets ?? []),
      ]),
    ]
      .join(" ")
      .replace(/\s+/g, " ");

    // The page uses the licence's words for the exclusion it echoes...
    expect(text).toMatch(/in no event shall the authors or copyright holders be liable/i);
    expect(text).toMatch(/arising from, out of or in connection with the software or its use/i);
    expect(text.toLowerCase()).toContain('provided "as is"');

    // ...grants nothing the licence withholds...
    for (const warranty of [/warrants that/i, /guarantees that/i, /we will refund/i, /free of defects/i]) {
      expect(text, `the terms must not promise ${warranty}`).not.toMatch(warranty);
    }

    // ...and cannot narrow the MIT grant, because it says the licence wins.
    expect(text).toMatch(/nothing on this page takes away a right the mit licence gives you/i);
    expect(text).toMatch(/where this page and LICENSE disagree, LICENSE wins/i);
  });

  it("ships that exclusion in the built /terms/ page", () => {
    const built = path.join(import.meta.dirname, "..", "out", "terms", "index.html");
    // The export is only present after `npm run build`; the render assertions
    // above already cover the content, so this is the extra check that the file
    // a crawler receives really carries the clause.
    if (!existsSync(built)) {
      return;
    }

    const html = readFileSync(built, "utf8");
    expect(html).toMatch(/is not liable for any damages of any kind/i);
    expect(html).toMatch(/local SQLite database/i);
    expect(html).toMatch(/cannot withdraw the disclaimer/i);
    expect(html).toMatch(/usage policy/i);
  });
});
