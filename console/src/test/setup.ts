import "@testing-library/jest-dom/vitest";

// jsdom 31 implements the CSS animation and transition properties, so Ant
// Design's motion layer (rc-motion) believes animations run and waits for
// animationend/transitionend events that jsdom never fires, leaving closed
// dialogs mounted. Removing the properties and event constructors makes
// rc-motion treat the environment as motion-free, as under older jsdom.
for (const name of ["AnimationEvent", "TransitionEvent"]) {
  Reflect.deleteProperty(window, name);
}
for (
  let proto = Object.getPrototypeOf(document.createElement("div").style);
  proto && proto !== Object.prototype;
  proto = Object.getPrototypeOf(proto)
) {
  for (const name of Object.getOwnPropertyNames(proto)) {
    if (/^(webkit|moz|ms|o)?(animation|transition)$/i.test(name)) {
      Reflect.deleteProperty(proto, name);
    }
  }
}

Object.defineProperty(window, "matchMedia", {
  configurable: true,
  value: (query: string) => ({
    matches: false,
    media: query,
    onchange: null,
    addListener: () => undefined,
    removeListener: () => undefined,
    addEventListener: () => undefined,
    removeEventListener: () => undefined,
    dispatchEvent: () => false,
  }),
});

class TestResizeObserver {
  observe() {}
  unobserve() {}
  disconnect() {}
}

Object.defineProperty(globalThis, "ResizeObserver", {
  configurable: true,
  value: TestResizeObserver,
});

// jsdom does not implement pseudo-element styles used for Ant Design's
// scrollbar measurement. Element styles are sufficient for component tests.
const getComputedStyle = window.getComputedStyle.bind(window);
Object.defineProperty(window, "getComputedStyle", {
  configurable: true,
  value: (element: Element) => getComputedStyle(element),
});
