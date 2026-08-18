const { Resolver } = require("@stoplight/spectral-ref-resolver");

module.exports = new Resolver({
  resolvers: {},
  transformRef({ ref, val }) {
    if (typeof val?.$ref !== "string" || !val.$ref.startsWith("#/")) {
      throw new Error("OpenAPI references must use document-internal JSON Pointers");
    }
    return ref;
  },
});
