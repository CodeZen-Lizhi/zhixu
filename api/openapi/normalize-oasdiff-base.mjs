import { readFileSync, writeFileSync } from "node:fs";

const [inputPath, outputPath] = process.argv.slice(2);
if (!inputPath || !outputPath || process.argv.length !== 4) {
  throw new Error("usage: node normalize-oasdiff-base.mjs <input> <output>");
}

const document = JSON.parse(readFileSync(inputPath, "utf8"));
if (document?.openapi !== "3.1.0" || !document.components?.schemas) {
  throw new Error("oasdiff base must be an OpenAPI 3.1 document with component schemas");
}

const booleanItems = [];
const visitSchema = (schema, path) => {
  if (!schema || typeof schema !== "object" || Array.isArray(schema)) return;
  if (typeof schema.items === "boolean") booleanItems.push([...path, "items"]);

  for (const key of ["items", "contains", "not", "if", "then", "else", "propertyNames", "additionalProperties", "unevaluatedProperties", "contentSchema"]) {
    visitSchema(schema[key], [...path, key]);
  }
  for (const key of ["allOf", "anyOf", "oneOf", "prefixItems"]) {
    for (const [index, child] of (schema[key] ?? []).entries()) visitSchema(child, [...path, key, index]);
  }
  for (const key of ["properties", "patternProperties", "dependentSchemas", "$defs", "definitions"]) {
    for (const [name, child] of Object.entries(schema[key] ?? {})) visitSchema(child, [...path, key, name]);
  }
};

for (const [name, schema] of Object.entries(document.components.schemas)) {
  visitSchema(schema, ["components", "schemas", name]);
}

const walkOpenAPI = (value, path = []) => {
  if (!value || typeof value !== "object") return;
  if (Array.isArray(value)) {
    value.forEach((child, index) => walkOpenAPI(child, [...path, index]));
    return;
  }
  for (const [key, child] of Object.entries(value)) {
    if (key === "schema" && !path.includes("schemas")) visitSchema(child, [...path, key]);
    walkOpenAPI(child, [...path, key]);
  }
};
walkOpenAPI(document.paths, ["paths"]);

const targetPath = ["components", "schemas", "WorkflowMergeComparisonReview", "properties", "categories", "items"];
const targetKey = JSON.stringify(targetPath);
for (const path of booleanItems) {
  if (JSON.stringify(path) !== targetKey) {
    throw new Error(`unsupported boolean items schema in oasdiff base at ${path.join(".")}`);
  }
}

const categories = document.components.schemas.WorkflowMergeComparisonReview?.properties?.categories;
if (booleanItems.length > 1) {
  throw new Error("oasdiff base contains duplicate boolean items schemas at the bootstrap target");
}
if (booleanItems.length === 1) {
  const categoryOrder = categories.prefixItems?.map((entry) => entry?.allOf?.[1]?.properties?.category?.const);
  const categoryRefs = categories.prefixItems?.map((entry) => entry?.allOf?.[0]?.$ref);
  if (categories.items !== false || categories.minItems !== 4 || categories.maxItems !== 4 ||
      categoryOrder?.join(",") !== "DUPLICATE,COMPLEMENTARY,CONFLICT,UNIQUE" ||
      categoryRefs?.every((ref) => ref === "#/components/schemas/WorkflowMergeCategory") !== true) {
    throw new Error("oasdiff base boolean items bootstrap shape drifted");
  }
  delete categories.items;
}

writeFileSync(outputPath, `${JSON.stringify(document, null, 2)}\n`, { flag: "wx" });
