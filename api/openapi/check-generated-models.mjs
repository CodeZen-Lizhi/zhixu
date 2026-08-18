import { readFileSync } from "node:fs";
import { resolve } from "node:path";

const generatedRoot = resolve(process.argv[2] ?? new URL("../../web/src/api/generated", import.meta.url).pathname);
const models = readFileSync(resolve(generatedRoot, "models/index.ts"), "utf8");
const forbidden = [
  /:\s*any\b/g,
  /\bas\s+any\b/g,
  /<any>/g,
  /\bArray<any>\b/g,
  /\bPromise<any>\b/g,
  /\bany\[\]/g,
  /\bSet</g,
];
const matches = forbidden.flatMap((pattern) => [...models.matchAll(pattern)].map((match) => match[0]));
if (matches.length) throw new Error(`generated public models contain unsafe types: ${[...new Set(matches)].join(", ")}`);
console.log("verified generated public models do not expose any or Set values");
