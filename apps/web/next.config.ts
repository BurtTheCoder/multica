import type { NextConfig } from "next";
import { config } from "dotenv";
import { resolve } from "path";

// Load root .env so REMOTE_API_URL is available to next.config.ts
config({ path: resolve(__dirname, "../../.env") });

const remoteApiUrl = process.env.REMOTE_API_URL || "http://localhost:8080";
const docsUrl = process.env.DOCS_URL || "http://localhost:4000";

// Parse hostnames from CORS_ALLOWED_ORIGINS so that Next.js dev server
// allows cross-origin HMR / webpack requests (e.g. from Tailscale hosts).
// Next expects bare hostnames here, not scheme-prefixed origins or host:port
// pairs. Keep localhost aliases too because Orie's local checks often use
// 127.0.0.1 while the app advertises localhost.
const allowedDevOrigins = Array.from(
  new Set([
    "localhost",
    "127.0.0.1",
    ...(process.env.CORS_ALLOWED_ORIGINS
      ? process.env.CORS_ALLOWED_ORIGINS.split(",")
          .map((origin) => {
            const trimmed = origin.trim();
            if (!trimmed) return "";
            try {
              return new URL(trimmed).hostname;
            } catch {
              return trimmed.split(":")[0] || trimmed;
            }
          })
      : []),
  ].filter(Boolean)),
);

const nextConfig: NextConfig = {
  ...(process.env.STANDALONE === "true" ? { output: "standalone" as const } : {}),
  transpilePackages: ["@multica/core", "@multica/ui", "@multica/views"],
  allowedDevOrigins,
  images: {
    formats: ["image/avif", "image/webp"],
    qualities: [75, 80, 85],
  },
  async rewrites() {
    return {
      // Run before file-system routes so /docs isn't shadowed by the
      // [workspaceSlug] dynamic segment.
      beforeFiles: [
        {
          source: "/docs",
          destination: `${docsUrl}/docs`,
        },
        {
          source: "/docs/:path*",
          destination: `${docsUrl}/docs/:path*`,
        },
      ],
      afterFiles: [
        {
          source: "/api/:path*",
          destination: `${remoteApiUrl}/api/:path*`,
        },
        {
          source: "/ws",
          destination: `${remoteApiUrl}/ws`,
        },
        {
          source: "/auth/:path*",
          destination: `${remoteApiUrl}/auth/:path*`,
        },
        {
          source: "/uploads/:path*",
          destination: `${remoteApiUrl}/uploads/:path*`,
        },
      ],
      fallback: [],
    };
  },
};

export default nextConfig;
