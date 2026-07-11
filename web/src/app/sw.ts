import { defaultCache } from "@serwist/next/worker";
import { CacheFirst, NetworkFirst, Serwist, StaleWhileRevalidate } from "serwist";

declare const self: ServiceWorkerGlobalScope & { __SW_MANIFEST: Array<unknown> };

const serwist = new Serwist({
  precacheEntries: self.__SW_MANIFEST,
  skipWaiting: true,
  clientsClaim: true,
  runtimeCaching: [
    ...defaultCache,
    {
      matcher: ({ request, url }) => request.destination === "document" || url.pathname === "/",
      handler: new NetworkFirst({ cacheName: "commute-shield-layout-documents" })
    },
    {
      matcher: ({ request }) => ["style", "script", "worker"].includes(request.destination),
      handler: new StaleWhileRevalidate({ cacheName: "commute-shield-layout-assets" })
    },
    {
      matcher: ({ request }) => ["font", "image"].includes(request.destination),
      handler: new CacheFirst({ cacheName: "commute-shield-static-assets" })
    }
  ]
});

serwist.addEventListeners();
