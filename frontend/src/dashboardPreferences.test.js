import test from "node:test";
import assert from "node:assert/strict";

import { DASHBOARD_REFRESH_OPTIONS } from "./dashboardPreferences.js";

test("dashboard refresh options do not poll the router faster than five seconds", () => {
	assert.deepEqual(DASHBOARD_REFRESH_OPTIONS, [0, 5_000, 10_000, 30_000, 60_000]);
});
