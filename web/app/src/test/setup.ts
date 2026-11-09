import "@testing-library/jest-dom/vitest";
import { cleanup } from "@testing-library/react";
import { afterEach, expect, vi } from "vitest";
import { takeUnhandled } from "./network";

// jsdom has <dialog> but none of its methods. This is the part of the
// browser's behaviour the app relies on: showModal opens it, close closes
// it and fires "close", and Escape inside it fires "cancel" and closes it.
if (!HTMLDialogElement.prototype.showModal) {
  HTMLDialogElement.prototype.showModal = function (this: HTMLDialogElement) {
    if (this.open) return;
    this.open = true;
    this.addEventListener("keydown", closeOnEscape);
  };
  HTMLDialogElement.prototype.close = function (this: HTMLDialogElement, returnValue?: string) {
    if (!this.open) return;
    this.open = false;
    this.removeEventListener("keydown", closeOnEscape);
    if (returnValue !== undefined) this.returnValue = returnValue;
    this.dispatchEvent(new Event("close"));
  };
}

function closeOnEscape(this: HTMLDialogElement, event: KeyboardEvent) {
  if (event.key !== "Escape") return;
  if (this.dispatchEvent(new Event("cancel", { cancelable: true }))) this.close();
}

afterEach(() => {
  cleanup();
  vi.useRealTimers();
  window.localStorage.clear();
  expect(takeUnhandled(), "requests no test handler answered").toEqual([]);
});
