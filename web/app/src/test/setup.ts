import "@testing-library/jest-dom/vitest";
import { cleanup } from "@testing-library/react";
import { afterEach, expect, vi } from "vitest";
import { takeUnhandled } from "./network";

afterEach(() => {
  cleanup();
  vi.useRealTimers();
  window.localStorage.clear();
  expect(takeUnhandled(), "requests no test handler answered").toEqual([]);
});
