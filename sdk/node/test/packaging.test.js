/**
 * What this package *claims* about itself, checked against what it ships.
 *
 * `package.json` said `"require": "./dist/index.cjs"` for as long as the
 * package has existed. `tsc` never emitted that file, so every
 * `require("@cloudcompiler/sdk")` failed with MODULE_NOT_FOUND -- and nothing
 * noticed, because the differential generator restricts *runnable* programs to
 * ESM (see `newJSModule` in internal/fuzz/node.go). CommonJS programs were
 * generated, parsed and IR-checked, and never once executed against the SDK.
 *
 * That is the same shape as the Node Lambda bundle that could not start: a
 * supported input nothing ever ran. So these assertions are deliberately about
 * the boring things -- do the files exist, does the entry import, does the
 * version agree -- because that is the class of claim that rots unwatched.
 */

import assert from "node:assert/strict";
import { execFileSync } from "node:child_process";
import { existsSync, mkdtempSync, cpSync, rmSync, readFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { test } from "node:test";
import { fileURLToPath } from "node:url";

const here = fileURLToPath(new URL(".", import.meta.url));
const root = join(here, "..");
const pkg = JSON.parse(readFileSync(join(root, "package.json"), "utf8"));

/** Every relative path the manifest points at. */
function declaredPaths() {
  const out = new Set([pkg.main, pkg.types]);
  for (const entry of Object.values(pkg.exports ?? {})) {
    for (const target of Object.values(entry)) out.add(target);
  }
  return [...out].filter(Boolean);
}

test("every path package.json points at exists", () => {
  for (const rel of declaredPaths()) {
    assert.ok(
      existsSync(join(root, rel)),
      `package.json points at ${rel}, which does not exist. ` +
        `This is how require() was broken: the manifest named a file the build never emitted.`,
    );
  }
});

test("every file listed in `files` exists", () => {
  for (const rel of pkg.files ?? []) {
    assert.ok(
      existsSync(join(root, rel)),
      `package.json lists ${rel} in "files", but it is not there, so it would be published missing`,
    );
  }
});

test("the package can be imported and required", () => {
  // Both conditions, because both are declared. `require` of an ES module
  // needs Node 22.12+, which is what the engines floor exists to say.
  const staging = mkdtempSync(join(tmpdir(), "cloudcc-pkg-"));
  try {
    const installed = join(staging, "node_modules", "@cloudcompiler", "sdk");
    cpSync(root, installed, {
      recursive: true,
      filter: (src) => !src.includes("node_modules"),
    });

    const probe = `
      const req = require("@cloudcompiler/sdk");
      if (typeof req.persist !== "function") throw new Error("require gave no persist");
      import("@cloudcompiler/sdk").then((esm) => {
        if (typeof esm.persist !== "function") throw new Error("import gave no persist");
        console.log("ok");
      }).catch((e) => { console.error(e); process.exit(1); });
    `;
    const out = execFileSync(process.execPath, ["-e", probe], {
      cwd: staging,
      encoding: "utf8",
    });
    assert.equal(out.trim(), "ok");
  } finally {
    rmSync(staging, { recursive: true, force: true });
  }
});

test("the declared engines floor is the one require(esm) needs", () => {
  // Not decoration. `require` resolves to the ESM entry, which only works from
  // Node 22.12 onward; on 22.0 it is ERR_REQUIRE_ESM. Saying so is the
  // difference between a clear install-time refusal and a confusing crash.
  assert.equal(pkg.engines?.node, ">=22.12");
  assert.equal(pkg.exports["."].require, "./dist/index.js");
});

test("this package and the Python SDK report the same version", () => {
  // They are two halves of one release; a user comparing them should not have
  // to work out which is authoritative. pyproject.toml said 0.1.0 while
  // `cloudcompiler.__version__` said 0.2.0, so `pip show` and the package
  // itself disagreed.
  const pyproject = readFileSync(
    join(root, "..", "python", "pyproject.toml"), "utf8",
  );
  const pyVersion = /^version = "([^"]+)"/m.exec(pyproject)?.[1];
  assert.equal(
    pkg.version, pyVersion,
    `@cloudcompiler/sdk is ${pkg.version} and cloudcompiler is ${pyVersion}`,
  );
});
