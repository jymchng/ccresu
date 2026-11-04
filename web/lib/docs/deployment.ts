import type { DocPage } from "./types";

export const deploymentDoc: DocPage = {
  slug: "deployment",
  title: "Deployment",
  summary:
    "Run ccresu as a user-level systemd service, or under any supervisor that can restart a process.",
  blocks: [
    {
      kind: "p",
      text: "ccresu is a long-running foreground process. It needs no privileges, writes only under your home directory, and handles SIGTERM by finishing its current step and releasing the database and the API port.",
    },
    {
      kind: "h2",
      text: "systemd (user unit)",
    },
    {
      kind: "code",
      label: "~/.config/systemd/user/ccresu.service",
      code: `[Unit]\nDescription=ccresu \u2014 resume Claude Code sessions after a usage limit resets\nDocumentation=https://github.com/jymchng/ccresu\nAfter=network-online.target\nWants=network-online.target\n\n[Service]\nType=simple\nExecStart=%h/.local/bin/ccresu daemon\nRestart=on-failure\nRestartSec=10\nEnvironment=CCRESU_APP__LOG_FORMAT=json\n\n[Install]\nWantedBy=default.target`,
    },
    {
      kind: "code",
      label: "Enable it",
      code: `systemctl --user daemon-reload\nsystemctl --user enable --now ccresu.service\nsystemctl --user status ccresu.service\nloginctl enable-linger "$USER"   # keep it running while you are logged out`,
    },
    {
      kind: "note",
      tone: "warning",
      title: "The daemon needs your Claude Code credentials",
      text: "Run the daemon as the user who owns `~/.claude`. A system-wide unit running as another user, or as root, will not see the sessions it is supposed to recover.",
    },
    {
      kind: "h2",
      text: "Running the API",
    },
    {
      kind: "p",
      text: "`ccresu serve` starts the REST + SSE API. It binds `127.0.0.1` unless the configuration says otherwise (`:8787` is promoted to `127.0.0.1:8787`, so a bare port cannot expose it by accident), and it writes a bearer token to `~/.config/ccresu/api-token` with mode 0600 on first start. Expose it beyond loopback only through a reverse proxy you control, and keep the token out of browser-visible code.",
    },
    {
      kind: "h2",
      text: "Upgrading",
    },
    {
      kind: "ol",
      items: [
        "Stop the daemon: `systemctl --user stop ccresu`.",
        "Download and verify the new release asset, replacing `~/.local/bin/ccresu`.",
        "Run `ccresu doctor` to confirm the database schema and the Claude Code CLI are readable.",
        "Start the daemon again and check `ccresu status`.",
      ],
    },
    {
      kind: "p",
      text: "The database is migrated forward automatically on start. Back up `~/.local/share/ccresu/ccresu.db` before a downgrade, since older binaries reject a newer schema.",
    },
    {
      kind: "h2",
      text: "Other supervisors",
    },
    {
      kind: "ul",
      items: [
        "macOS: a launchd `LaunchAgent` running the same `ccresu daemon` command.",
        "Windows: a Task Scheduler task set to run at logon with `ccresu daemon`.",
        "Containers: mount `~/.claude` and the data directory as volumes, and run the daemon in the foreground; the SQLite driver is pure Go, so there is no libc dependency.",
        "Supervisors that stop the process with SIGKILL skip the graceful shutdown; the state machine is crash-safe, so the next start reconciles the stale attempt.",
      ],
    },
  ],
};
