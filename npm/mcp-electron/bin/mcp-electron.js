#!/usr/bin/env node
// Runs the mcp-electron binary of this platform, installed by the package
// manager as one of the optionalDependencies. stdio is inherited so the MCP
// host talks to the Go server directly: closing the host's pipe still reaches
// the server as EOF on stdin and stops the applications it launched.
"use strict";

const { spawn } = require("node:child_process");
const fs = require("node:fs");

// Keep in sync with TARGETS in scripts/release.ts.
const TARGETS = {
  "darwin-arm64": "mcp-electron",
  "darwin-x64": "mcp-electron",
  "win32-arm64": "mcp-electron.exe",
  "win32-x64": "mcp-electron.exe",
};

function fail(message) {
  process.stderr.write(`mcp-electron: ${message}\n`);
  process.exit(1);
}

function binaryPath() {
  const key = `${process.platform}-${process.arch}`;
  const file = TARGETS[key];
  if (!file) {
    fail(
      `no prebuilt binary for ${key} (available: ${Object.keys(TARGETS).join(", ")}). ` +
        "Build from source: go install github.com/dev-toolings/mcp-electron/cmd/mcp-electron@latest",
    );
  }
  const pkg = `@dev-toolings/mcp-electron-${key}`;
  try {
    return require.resolve(`${pkg}/bin/${file}`);
  } catch {
    fail(`${pkg} is not installed. Reinstall without --omit=optional / --no-optional.`);
  }
}

const bin = binaryPath();

// Some package managers drop the executable bit when they extract a tarball.
if (process.platform !== "win32") {
  try {
    fs.accessSync(bin, fs.constants.X_OK);
  } catch {
    try {
      fs.chmodSync(bin, 0o755);
    } catch (err) {
      fail(`${bin} is not executable and chmod failed: ${err.message}`);
    }
  }
}

const child = spawn(bin, process.argv.slice(2), { stdio: "inherit", windowsHide: true });

// Forward termination so the server tears down its Electron process trees
// before this launcher exits.
for (const signal of ["SIGINT", "SIGTERM", "SIGHUP"]) {
  process.on(signal, () => child.kill(signal));
}

child.on("error", (err) => fail(`cannot start ${bin}: ${err.message}`));
child.on("exit", (code, signal) => {
  if (signal) {
    process.removeAllListeners(signal);
    process.kill(process.pid, signal);
    return;
  }
  process.exit(code ?? 1);
});
