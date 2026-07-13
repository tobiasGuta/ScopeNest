const listeners = [];

globalThis.chrome = {
  runtime: {
    id: "nnmpnmnmmfoedjeionoopgnbjnepfolh",
    async sendMessage(message) {
      if (message?.type === "native-request") {
        return { success: false, error: "Preview: native host is not connected", errorCode: "NATIVE_HOST_UNAVAILABLE" };
      }
      return { connected: false, error: "Preview mode" };
    },
    onMessage: { addListener(listener) { listeners.push(listener); } },
  },
  storage: {
    local: {
      values: {},
      async get(key) { return { [key]: this.values[key] }; },
      async set(values) { Object.assign(this.values, values); },
    },
  },
  tabs: { async query() { return [{ id: 1, windowId: 1, url: "https://example.com/" }]; } },
  sidePanel: { async open() {} },
};
