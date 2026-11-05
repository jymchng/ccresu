import type { ReactElement } from "react";

import type { DiagramId } from "@/lib/docs/types";
import { cn } from "@/lib/utils";

/**
 * Hand-authored SVG figures.
 *
 * Why inline SVG rather than a PNG in `public/`:
 *
 *  - it is markup, so it scales without a second asset per breakpoint, stays
 *    sharp on a 3x phone screen, and adds nothing binary to the repository;
 *  - its colours come from the same CSS variables as the rest of the site, so
 *    light and dark mode work without a second drawing;
 *  - every label is real text in the document (selectable, searchable, and
 *    readable by a screen reader through `<title>`/`<desc>`), which a bitmap
 *    can never be.
 *
 * The geometry is deliberately vertical and narrow. A wide left-to-right
 * diagram is the usual choice on a documentation site and the wrong one here:
 * below roughly 380px it either shrinks to illegible type or forces the page to
 * scroll sideways. These stack downward and are width-capped, so 320px is
 * comfortable rather than survivable.
 */

const MONO = "font-mono";

/** Shared box geometry for the state machine: one column, growing downward. */
const NODE = { x: 20, width: 230, height: 34, gap: 18, centre: 135 } as const;

const STATE_CHAIN = [
  "DISCOVERED",
  "USAGE_LIMIT_DETECTED",
  "WAITING_FOR_RESET",
  "RESET_TIME_REACHED",
  "CHECKING_SESSION",
  "CHECKING_LIMIT",
  "RESUMING",
] as const;

/** y of the top edge of chain node `index`. */
function chainY(index: number): number {
  return 8 + index * (NODE.height + NODE.gap);
}

const CHAIN_BOTTOM = chainY(STATE_CHAIN.length - 1) + NODE.height;
const CHIP_Y = CHAIN_BOTTOM + 22;
const CHIP_H = 32;
const LEGEND_Y = CHIP_Y + CHIP_H + 44;
const STATE_MACHINE_HEIGHT = LEGEND_Y + 26;

/** An arrowhead, drawn once per diagram with a unique id. */
function Arrowhead({ id, colour }: { id: string; colour: string }) {
  return (
    <marker
      id={id}
      viewBox="0 0 10 10"
      refX="8"
      refY="5"
      markerWidth="6"
      markerHeight="6"
      orient="auto-start-reverse"
    >
      <path d="M 0 0 L 10 5 L 0 10 z" fill={colour} />
    </marker>
  );
}

/**
 * The recovery lifecycle: the chain every session walks, the retry loop that
 * goes back up it, and the two ways out.
 */
function StateMachine({ titleId, descriptionId, label, description }: DiagramProps) {
  const solid = `url(#diagram-state-machine-solid)`;
  const dashed = `url(#diagram-state-machine-dashed)`;
  const resumingY = chainY(STATE_CHAIN.length - 1);
  const checkingSessionY = chainY(4);

  return (
    <svg
      role="img"
      aria-labelledby={titleId}
      aria-describedby={descriptionId}
      viewBox={`0 0 340 ${STATE_MACHINE_HEIGHT}`}
      className="mx-auto h-auto w-full max-w-[22rem]"
      preserveAspectRatio="xMidYMid meet"
    >
      <title id={titleId}>{label}</title>
      <desc id={descriptionId}>{description}</desc>

      <defs>
        <Arrowhead id="diagram-state-machine-solid" colour="var(--muted-foreground)" />
        <Arrowhead id="diagram-state-machine-dashed" colour="var(--muted-foreground)" />
      </defs>

      {STATE_CHAIN.map((state, index) => {
        const y = chainY(index);
        return (
          <g key={state}>
            <rect
              x={NODE.x}
              y={y}
              width={NODE.width}
              height={NODE.height}
              rx="8"
              fill="var(--card)"
              stroke="var(--border)"
            />
            <text
              x={NODE.centre}
              y={y + NODE.height / 2 + 4}
              textAnchor="middle"
              fontSize="12"
              fill="var(--foreground)"
              className={MONO}
            >
              {state}
            </text>
            {index < STATE_CHAIN.length - 1 ? (
              <line
                x1={NODE.centre}
                y1={y + NODE.height}
                x2={NODE.centre}
                y2={y + NODE.height + NODE.gap - 3}
                stroke="var(--muted-foreground)"
                strokeWidth="1.25"
                markerEnd={solid}
              />
            ) : null}
          </g>
        );
      })}

      {/* The retry loop: a failed attempt with budget left climbs back up. */}
      <polyline
        points={`${NODE.x + NODE.width},${resumingY + NODE.height / 2} 300,${resumingY + NODE.height / 2} 300,${checkingSessionY + NODE.height / 2} ${NODE.x + NODE.width + 3},${checkingSessionY + NODE.height / 2}`}
        fill="none"
        stroke="var(--muted-foreground)"
        strokeWidth="1.25"
        strokeDasharray="5 4"
        markerEnd={dashed}
      />

      {/* The two ways out of RESUMING. */}
      <line
        x1={NODE.centre}
        y1={CHAIN_BOTTOM}
        x2={72}
        y2={CHIP_Y - 3}
        stroke="var(--muted-foreground)"
        strokeWidth="1.25"
        markerEnd={solid}
      />
      <rect
        x={20}
        y={CHIP_Y}
        width={104}
        height={CHIP_H}
        rx="8"
        fill="var(--card)"
        stroke="var(--primary)"
      />
      <text
        x={72}
        y={CHIP_Y + CHIP_H / 2 + 4}
        textAnchor="middle"
        fontSize="11.5"
        fill="var(--primary)"
        className={MONO}
      >
        RESUMED
      </text>

      <line
        x1={NODE.centre}
        y1={CHAIN_BOTTOM}
        x2={230}
        y2={CHIP_Y - 3}
        stroke="var(--muted-foreground)"
        strokeWidth="1.25"
        strokeDasharray="5 4"
        markerEnd={dashed}
      />
      <rect
        x={140}
        y={CHIP_Y}
        width={180}
        height={CHIP_H}
        rx="8"
        fill="var(--card)"
        stroke="var(--border)"
        strokeDasharray="5 4"
      />
      <text
        x={230}
        y={CHIP_Y + CHIP_H / 2 + 4}
        textAnchor="middle"
        fontSize="11"
        fill="var(--foreground)"
        className={MONO}
      >
        PERMANENTLY_FAILED
      </text>

      {/* The operator edge out of it. */}
      <line
        x1={230}
        y1={CHIP_Y + CHIP_H}
        x2={230}
        y2={LEGEND_Y - 12}
        stroke="var(--muted-foreground)"
        strokeWidth="1.25"
        strokeDasharray="5 4"
        markerEnd={dashed}
      />
      <text x={222} y={LEGEND_Y - 2} textAnchor="end" fontSize="9.5" fill="var(--muted-foreground)" className={MONO}>
        ccresu reset &lt;id&gt;
      </text>
      <rect
        x={140}
        y={LEGEND_Y}
        width={180}
        height={CHIP_H}
        rx="8"
        fill="var(--card)"
        stroke="var(--border)"
      />
      <text
        x={230}
        y={LEGEND_Y + CHIP_H / 2 + 4}
        textAnchor="middle"
        fontSize="11"
        fill="var(--foreground)"
        className={MONO}
      >
        DISABLED
      </text>

      {/* A legend, because a dashed line nobody explains is decoration. */}
      <line
        x1={20}
        y1={LEGEND_Y + CHIP_H + 18}
        x2={52}
        y2={LEGEND_Y + CHIP_H + 18}
        stroke="var(--muted-foreground)"
        strokeWidth="1.25"
        strokeDasharray="5 4"
      />
      <text
        x={60}
        y={LEGEND_Y + CHIP_H + 21}
        fontSize="9.5"
        fill="var(--muted-foreground)"
      >
        retry, or the operator edge
      </text>
    </svg>
  );
}

/** What every drawing needs: an accessible name, a description, and anchor ids. */
type DiagramProps = {
  titleId: string;
  descriptionId: string;
  label: string;
  description: string;
};

type Step = {
  /** The command as the CLI receives it, broken where a phone would break it. */
  command: readonly string[];
  /** One or two short lines of what this call did. */
  note: readonly string[];
};

const STOP_RESUME_STEPS: readonly Step[] = [
  {
    command: ['claude -p --resume <id>', '--output-format json "<message>"'],
    note: [
      "refused: the session is still attached",
      "to Claude Code's background agent",
    ],
  },
  {
    command: ["claude stop <id>"],
    note: [
      "the agent releases it. Written to the log,",
      "never to the attempt row",
    ],
  },
  {
    command: ['claude -p --resume <id>', '--output-format json "<message>"'],
    note: [
      "byte-identical arguments, same attempt:",
      "the result becomes the attempt's result",
    ],
  },
];

const STEP = { x: 20, width: 300, centre: 170, arrow: 20, noteGap: 8, lineHeight: 13 };

/**
 * Stop, then resume: what happens when the CLI refuses because a background
 * agent still holds the session. Three calls, one attempt.
 */
function StopThenResume({ titleId, descriptionId, label, description }: DiagramProps) {
  const stroke = "var(--muted-foreground)";
  const elements: ReactElement[] = [];
  let y = 8;

  STOP_RESUME_STEPS.forEach((step, index) => {
    const height = 24 + step.command.length * 16;

    elements.push(
      <g key={`step-${index}`}>
        <rect
          x={STEP.x}
          y={y}
          width={STEP.width}
          height={height}
          rx="8"
          fill="var(--card)"
          stroke="var(--border)"
        />
        {step.command.map((line, lineIndex) => (
          <text
            key={lineIndex}
            x={STEP.centre}
            y={y + 20 + lineIndex * 16}
            textAnchor="middle"
            fontSize="11.5"
            fill="var(--foreground)"
            className={MONO}
          >
            {line}
          </text>
        ))}
      </g>,
    );
    y += height;

    // The arrow and its explanation, between one call and the next.
    elements.push(
      <line
        key={`arrow-${index}`}
        x1={STEP.centre}
        y1={y}
        x2={STEP.centre}
        y2={y + STEP.arrow - 3}
        stroke={stroke}
        strokeWidth="1.25"
        markerEnd="url(#diagram-stop-then-resume-arrow)"
      />,
    );
    y += STEP.arrow;

    step.note.forEach((line, lineIndex) => {
      elements.push(
        <text
          key={`note-${index}-${lineIndex}`}
          x={STEP.centre}
          y={y + 10 + lineIndex * STEP.lineHeight}
          textAnchor="middle"
          fontSize="9.5"
          fill="var(--muted-foreground)"
        >
          {line}
        </text>,
      );
    });
    y += step.note.length * STEP.lineHeight + STEP.noteGap;
  });

  const resultHeight = 46;

  return (
    <svg
      role="img"
      aria-labelledby={titleId}
      aria-describedby={descriptionId}
      viewBox={`0 0 340 ${y + resultHeight + 8}`}
      className="mx-auto h-auto w-full max-w-[22rem]"
      preserveAspectRatio="xMidYMid meet"
    >
      <title id={titleId}>{label}</title>
      <desc id={descriptionId}>{description}</desc>

      <defs>
        <Arrowhead id="diagram-stop-then-resume-arrow" colour={stroke} />
      </defs>

      {elements}

      <rect
        x={STEP.x}
        y={y}
        width={STEP.width}
        height={resultHeight}
        rx="8"
        fill="var(--card)"
        stroke="var(--primary)"
      />
      <text
        x={STEP.centre}
        y={y + 20}
        textAnchor="middle"
        fontSize="12"
        fill="var(--primary)"
        className={MONO}
      >
        RESUMED
      </text>
      <text
        x={STEP.centre}
        y={y + 36}
        textAnchor="middle"
        fontSize="9.5"
        fill="var(--muted-foreground)"
      >
        verified: same session id, transcript grew
      </text>
    </svg>
  );
}

const DIAGRAMS: Record<DiagramId, (props: DiagramProps) => ReactElement> = {
  "state-machine": StateMachine,
  "stop-then-resume": StopThenResume,
};

/**
 * One documentation figure: an inline SVG with an accessible name and
 * description, and a visible caption carrying the same words. The caption is
 * what a sighted reader sees under the drawing; `<desc>` is what a screen
 * reader announces. They are written once, in the content, and used twice.
 */
export function DocDiagram({
  id,
  label,
  description,
  className,
}: {
  id: DiagramId;
  label: string;
  description: string;
  className?: string;
}) {
  const Drawing = DIAGRAMS[id];
  const slug = `diagram-${id}`;

  return (
    <figure
      aria-labelledby={`${slug}-caption`}
      className={cn(
        "overflow-hidden rounded-xl border border-border bg-card p-3 sm:p-4",
        className,
      )}
    >
      <div className="overflow-x-auto">
        <Drawing
          titleId={`${slug}-title`}
          descriptionId={`${slug}-desc`}
          label={label}
          description={description}
        />
      </div>
      <figcaption
        id={`${slug}-caption`}
        className="mt-3 flex flex-col gap-1 border-t border-border pt-3"
      >
        <span className="text-xs font-semibold text-foreground">{label}</span>
        <span className="text-xs leading-relaxed text-muted-foreground">
          {description}
        </span>
      </figcaption>
    </figure>
  );
}
