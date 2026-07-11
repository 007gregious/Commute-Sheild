"use client";

import { Serwist } from "@serwist/window";
import { useEffect } from "react";

export function ServiceWorkerRegistrar() {
  useEffect(() => {
    if ("serviceWorker" in navigator && process.env.NODE_ENV === "production") {
      const serwist = new Serwist("/sw.js");
      serwist.register().catch((error) => console.error("[pwa] service worker registration failed", error));
    }
  }, []);

  return null;
}
