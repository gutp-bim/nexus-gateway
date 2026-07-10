// Copyright 2026 nexus-gateway contributors
// SPDX-License-Identifier: Apache-2.0

// Unmount React trees rendered by @testing-library/react after each test so a
// file with multiple component tests doesn't accumulate DOM (which makes
// getByRole/getByText see duplicates across tests).
import { afterEach } from "vitest";
import { cleanup } from "@testing-library/react";

afterEach(() => {
  cleanup();
});
