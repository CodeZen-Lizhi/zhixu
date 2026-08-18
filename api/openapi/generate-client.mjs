import { createHash } from "node:crypto";
import {
  cpSync,
  existsSync,
  mkdtempSync,
  mkdirSync,
  readFileSync,
  readdirSync,
  rmSync,
  statSync,
  writeFileSync,
} from "node:fs";
import { tmpdir } from "node:os";
import { dirname, relative, resolve } from "node:path";
import { spawnSync } from "node:child_process";
import { fileURLToPath } from "node:url";

const scriptDirectory = dirname(fileURLToPath(import.meta.url));
const repositoryRoot = resolve(scriptDirectory, "../..");
const generatedRoot = resolve(repositoryRoot, "web/src/api/generated");
const expectedRelativeOutput = "web/src/api/generated";
if (relative(repositoryRoot, generatedRoot) !== expectedRelativeOutput) throw new Error("refusing unexpected generated output path");

const checkOnly = process.argv.includes("--check");
const temporaryRoot = mkdtempSync(resolve(tmpdir(), "zhixu-openapi-generate-"));
const normalizedInput = resolve(temporaryRoot, "openapi.generator.json");
const temporaryOutput = resolve(temporaryRoot, "client");
const generatorBinary = resolve(scriptDirectory, "node_modules/.bin/openapi-generator-cli");

function run(command, args, options = {}) {
  const result = spawnSync(command, args, {
    cwd: scriptDirectory,
    encoding: "utf8",
    maxBuffer: 64 * 1024 * 1024,
    ...options,
  });
  if (result.error) throw result.error;
  if (result.status !== 0) {
    process.stderr.write(result.stdout ?? "");
    process.stderr.write(result.stderr ?? "");
    throw new Error(`${command} exited with status ${result.status}`);
  }
  return `${result.stdout ?? ""}${result.stderr ?? ""}`;
}

function verifyWarnings(log) {
  const baseline = JSON.parse(readFileSync(resolve(scriptDirectory, "generator-warning-baseline.json"), "utf8"));
  if (baseline.generatorVersion !== "7.24.0") throw new Error("generator warning baseline version drifted");
  const warnings = log.split(/\r?\n/).filter((line) => line.includes(" WARN "));
  const unmatched = warnings.filter((line) => !baseline.warnings.some(({ contains }) => line.includes(contains)));
  if (unmatched.length) throw new Error(`unreviewed generator warnings:\n${unmatched.join("\n")}`);
  for (const expected of baseline.warnings) {
    const actual = warnings.filter((line) => line.includes(expected.contains)).length;
    if (actual !== expected.count) {
      throw new Error(`generator warning count drift for ${expected.contains}: expected ${expected.count}, got ${actual}`);
    }
  }
  console.log(`verified ${warnings.length} generator warnings against the 7.24.0 baseline`);
}

function digest(path) {
  return createHash("sha256").update(readFileSync(path)).digest("hex");
}

function generatedMetadata() {
  return {
    generator: "typescript-fetch",
    generatorVersion: "7.24.0",
    wrapperVersion: "2.40.1",
    authoritativeContractSha256: digest(resolve(scriptDirectory, "openapi.json")),
    normalizerManifestSha256: digest(resolve(scriptDirectory, "generator-input-manifest.json")),
    generatorConfigSha256: digest(resolve(scriptDirectory, "typescript-fetch.config.json")),
    runtimeTemplateSha256: digest(resolve(scriptDirectory, "templates/typescript-fetch/runtime.mustache")),
    oneOfTemplateSha256: digest(resolve(scriptDirectory, "templates/typescript-fetch/modelOneOfInterfaces.mustache")),
  };
}

function listFiles(root, prefix = "") {
  if (!existsSync(root)) return [];
  return readdirSync(root).flatMap((name) => {
    const absolute = resolve(root, name);
    const child = prefix ? `${prefix}/${name}` : name;
    return statSync(absolute).isDirectory() ? listFiles(absolute, child) : [child];
  }).sort();
}

function normalizeGeneratedWhitespace(root) {
  for (const path of listFiles(root)) {
    if (!/\.(?:gitignore|json|md|ts)$/.test(path)) continue;
    const absolute = resolve(root, path);
    const source = readFileSync(absolute, "utf8");
    const normalized = source.replace(/[ \t]+(?=\r?\n)/g, "");
    if (normalized !== source) writeFileSync(absolute, normalized);
  }
}

function compareGenerated(expectedRoot, actualRoot) {
  const expectedFiles = listFiles(expectedRoot);
  const actualFiles = listFiles(actualRoot);
  const differences = [];
  for (const path of new Set([...expectedFiles, ...actualFiles])) {
    if (!expectedFiles.includes(path)) differences.push(`missing committed file: ${path}`);
    else if (!actualFiles.includes(path)) differences.push(`stale committed file: ${path}`);
    else if (!readFileSync(resolve(expectedRoot, path)).equals(readFileSync(resolve(actualRoot, path)))) differences.push(`content drift: ${path}`);
  }
  if (differences.length) throw new Error(`generated OpenAPI client drifted:\n${differences.slice(0, 50).join("\n")}`);
}

try {
  const normalizerReport = run(process.execPath, [
    resolve(scriptDirectory, "normalize-generator-input.mjs"),
    resolve(scriptDirectory, "openapi.json"),
    normalizedInput,
  ]);
  process.stdout.write(normalizerReport);
  mkdirSync(temporaryOutput);
  const generatorLog = run(generatorBinary, [
    "generate",
    "-g", "typescript-fetch",
    "-i", normalizedInput,
    "-o", temporaryOutput,
    "-c", resolve(scriptDirectory, "typescript-fetch.config.json"),
    "-t", resolve(scriptDirectory, "templates/typescript-fetch"),
    "--global-property", "apiDocs=false,modelDocs=false,apiTests=false,modelTests=false",
    "--type-mappings", "Null=null",
    "--skip-operation-example",
    "--strict-spec", "true",
  ]);
  verifyWarnings(generatorLog);
  normalizeGeneratedWhitespace(temporaryOutput);
  run(process.execPath, [resolve(scriptDirectory, "check-generated-models.mjs"), temporaryOutput], { stdio: "pipe" });
  writeFileSync(resolve(temporaryOutput, "GENERATED.md"), [
    "# Generated OpenAPI client",
    "",
    "This directory is generated from `api/openapi/openapi.json` by `make openapi-generate`.",
    "Do not edit files here. Put transport and domain behavior in the project-owned API boundary.",
    "",
  ].join("\n"));
  writeFileSync(resolve(temporaryOutput, ".generated-contract.json"), `${JSON.stringify(generatedMetadata(), null, 2)}\n`);

  if (checkOnly) {
    compareGenerated(generatedRoot, temporaryOutput);
    console.log("generated OpenAPI client matches the committed output");
  } else {
    rmSync(generatedRoot, { recursive: true, force: true });
    mkdirSync(dirname(generatedRoot), { recursive: true });
    cpSync(temporaryOutput, generatedRoot, { recursive: true });
    console.log(`generated OpenAPI client at ${expectedRelativeOutput}`);
  }
} finally {
  rmSync(temporaryRoot, { recursive: true, force: true });
}
