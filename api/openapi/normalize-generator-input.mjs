import { readFileSync, writeFileSync } from "node:fs";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";

const scriptDirectory = dirname(fileURLToPath(import.meta.url));
const inputPath = resolve(process.argv[2] ?? resolve(scriptDirectory, "openapi.json"));
const outputPath = process.argv[3] ? resolve(process.argv[3]) : null;
if (!outputPath) throw new Error("usage: node normalize-generator-input.mjs <input> <output>");

const manifest = JSON.parse(readFileSync(resolve(scriptDirectory, "generator-input-manifest.json"), "utf8"));
const document = JSON.parse(readFileSync(inputPath, "utf8"));
if (document.openapi !== "3.1.0") throw new Error(`generator input requires OpenAPI 3.1.0, got ${document.openapi}`);
if (manifest.generatorVersion !== "7.24.0" || manifest.version !== 1) throw new Error("unsupported generator-input manifest version");

const openJsonAllowlist = new Set(manifest.transforms.openJsonObjects);
const emptySchemaAllowlist = new Set(manifest.transforms.unconstrainedJsonSchemas);
const seenOpenJson = new Set();
const seenEmptySchemas = new Set();
const constTransforms = [];
const uniqueItemsTransforms = [];

function escapePointerPart(value) {
  return value.replaceAll("~", "~0").replaceAll("/", "~1");
}

function pointerChild(pointer, key) {
  return `${pointer}/${escapePointerPart(key)}`;
}

function walkSchema(schema, pointer, visitor) {
  if (typeof schema === "boolean") {
    visitor(schema, pointer);
    return;
  }
  if (!schema || typeof schema !== "object" || Array.isArray(schema)) throw new Error(`invalid schema at ${pointer}`);
  visitor(schema, pointer);
  for (const keyword of ["not", "if", "then", "else", "contains", "propertyNames", "additionalProperties"]) {
    if (schema[keyword] && typeof schema[keyword] === "object" && !Array.isArray(schema[keyword])) {
      walkSchema(schema[keyword], pointerChild(pointer, keyword), visitor);
    }
  }
  if (schema.items && typeof schema.items === "object" && !Array.isArray(schema.items)) {
    walkSchema(schema.items, pointerChild(pointer, "items"), visitor);
  } else if (typeof schema.items === "boolean") {
    visitor(schema.items, pointerChild(pointer, "items"));
  }
  for (const keyword of ["allOf", "anyOf", "oneOf", "prefixItems"]) {
    for (const [index, child] of (schema[keyword] ?? []).entries()) {
      walkSchema(child, pointerChild(pointerChild(pointer, keyword), String(index)), visitor);
    }
  }
  for (const keyword of ["properties", "patternProperties", "$defs", "dependentSchemas"]) {
    for (const [key, child] of Object.entries(schema[keyword] ?? {})) {
      walkSchema(child, pointerChild(pointerChild(pointer, keyword), key), visitor);
    }
  }
}

function schemaRoots(openapi) {
  const roots = [];
  for (const [name, schema] of Object.entries(openapi.components?.schemas ?? {})) {
    roots.push([schema, `#/components/schemas/${escapePointerPart(name)}`]);
  }
  function findInlineSchemas(value, pointer) {
    if (!value || typeof value !== "object") return;
    for (const [key, child] of Object.entries(value)) {
      const childPointer = pointerChild(pointer, key);
      if (key === "schema") roots.push([child, childPointer]);
      else findInlineSchemas(child, childPointer);
    }
  }
  findInlineSchemas(openapi.paths ?? {}, "#/paths");
  for (const section of ["parameters", "requestBodies", "responses", "headers"]) {
    findInlineSchemas(openapi.components?.[section] ?? {}, `#/components/${section}`);
  }
  return roots;
}

function assertExactSet(actual, expected, label) {
  const missing = [...expected].filter((pointer) => !actual.has(pointer));
  const unexpected = [...actual].filter((pointer) => !expected.has(pointer));
  if (missing.length || unexpected.length) {
    throw new Error(`${label} manifest drift: missing=${missing.join(",")}, unexpected=${unexpected.join(",")}`);
  }
}

function primitiveType(value, pointer) {
  if (value === null) return "null";
  if (typeof value === "string") return "string";
  if (typeof value === "boolean") return "boolean";
  if (typeof value === "number" && Number.isFinite(value)) return Number.isInteger(value) ? "integer" : "number";
  throw new Error(`unsupported const value at ${pointer}`);
}

function transformPrimitiveConst(schema, pointer) {
  if (!Object.hasOwn(schema, "const")) return;
  if (schema.enum !== undefined) throw new Error(`const and enum cannot be normalized together at ${pointer}`);
  const inferredType = primitiveType(schema.const, pointer);
  if (schema.type !== undefined && schema.type !== inferredType && !(schema.type === "number" && inferredType === "integer")) {
    throw new Error(`const type mismatch at ${pointer}`);
  }
  schema.type ??= inferredType;
  schema.enum = [schema.const];
  delete schema.const;
  constTransforms.push(pointer);
}

function transformUniqueItems(schema, pointer) {
  if (!Object.hasOwn(schema, "uniqueItems")) return;
  if (schema.type !== "array" || schema.uniqueItems !== true) {
    throw new Error(`unsupported uniqueItems schema at ${pointer}`);
  }
  delete schema.uniqueItems;
  uniqueItemsTransforms.push(pointer);
}

function splitWorkflowTopicOutlineSection(openapi) {
  const pointer = manifest.transforms.namedUnion;
  if (pointer !== "#/components/schemas/WorkflowTopicOutlineSection") throw new Error(`unsupported named union transform ${pointer}`);
  const original = openapi.components?.schemas?.WorkflowTopicOutlineSection;
  if (!original || original.type !== "object" || original.oneOf?.length !== 2 || original.additionalProperties !== false) {
    throw new Error("WorkflowTopicOutlineSection no longer matches the reviewed union shape");
  }
  const [supportedConstraint, gapConstraint] = original.oneOf;
  const supported = structuredClone(original);
  delete supported.oneOf;
  supported.properties.supports = {
    ...supported.properties.supports,
    ...supportedConstraint.properties.supports,
  };
  supported.properties.gap_code = structuredClone(supportedConstraint.properties.gap_code);
  const gap = structuredClone(original);
  delete gap.oneOf;
  gap.properties.supports = {
    ...gap.properties.supports,
    ...gapConstraint.properties.supports,
  };
  gap.properties.gap_code = structuredClone(gapConstraint.properties.gap_code);
  openapi.components.schemas.WorkflowTopicOutlineSectionSupported = {
    ...supported,
  };
  openapi.components.schemas.WorkflowTopicOutlineSectionGap = {
    ...gap,
  };
  openapi.components.schemas.WorkflowTopicOutlineSection = {
    oneOf: [
      { $ref: "#/components/schemas/WorkflowTopicOutlineSectionSupported" },
      { $ref: "#/components/schemas/WorkflowTopicOutlineSectionGap" },
    ],
  };
}

splitWorkflowTopicOutlineSection(document);
const roots = schemaRoots(document);
const visited = new Set();
for (const [schema, pointer] of roots) {
  walkSchema(schema, pointer, (current, currentPointer) => {
    if (visited.has(currentPointer)) return;
    visited.add(currentPointer);
    if (typeof current === "boolean") {
      if (currentPointer !== "#/components/schemas/WorkflowMergeComparisonReview/properties/categories/items" || current !== false) {
        throw new Error(`unsupported boolean schema at ${currentPointer}`);
      }
      return;
    }
    if (Object.keys(current).length === 0) {
      seenEmptySchemas.add(currentPointer);
      if (!emptySchemaAllowlist.has(currentPointer)) throw new Error(`unregistered unconstrained schema at ${currentPointer}`);
      Object.assign(current, { $ref: "#/components/schemas/JSONValue" });
      return;
    }
    if (current.additionalProperties === true) {
      seenOpenJson.add(currentPointer);
      if (!openJsonAllowlist.has(currentPointer)) throw new Error(`unregistered open JSON object at ${currentPointer}`);
      current.additionalProperties = { $ref: "#/components/schemas/JSONValue" };
    }
    transformUniqueItems(current, currentPointer);
    transformPrimitiveConst(current, currentPointer);
  });
}
assertExactSet(seenOpenJson, openJsonAllowlist, "open JSON object");
assertExactSet(seenEmptySchemas, emptySchemaAllowlist, "unconstrained JSON schema");
if (uniqueItemsTransforms.length !== manifest.transforms.uniqueItems.expectedCount) {
  throw new Error(`uniqueItems manifest drift: expected=${manifest.transforms.uniqueItems.expectedCount}, actual=${uniqueItemsTransforms.length}`);
}

if (document.components.schemas.JSONValue !== undefined) throw new Error("authoritative OpenAPI must not define generator-only JSONValue");
document.components.schemas.JSONValue = {
  oneOf: [
    { type: "null" },
    { type: "boolean" },
    { type: "number" },
    { type: "string" },
    { type: "array", items: { $ref: "#/components/schemas/JSONValue" } },
    { type: "object", additionalProperties: { $ref: "#/components/schemas/JSONValue" } },
  ],
};

writeFileSync(outputPath, `${JSON.stringify(document, null, 2)}\n`);
console.log(JSON.stringify({
  generatorVersion: manifest.generatorVersion,
  primitiveConst: constTransforms.length,
  uniqueItemsArrays: uniqueItemsTransforms.length,
  openJsonObjects: [...seenOpenJson].sort(),
  unconstrainedJsonSchemas: [...seenEmptySchemas].sort(),
  namedUnion: manifest.transforms.namedUnion,
}, null, 2));
