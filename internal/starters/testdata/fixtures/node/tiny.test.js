const test = require("node:test");
const assert = require("node:assert");

test("add", () => assert.strictEqual(2 + 3, 5));
test("add negative", () => assert.strictEqual(-1 + 1, 0));
