// API fixtures, typed by the types generated from the Go structs: a field
// renamed in Go breaks these at the type check, not at runtime.
import type { App, Deployment } from "@/api/generated/domain";
import type { LoginInfo } from "@/api/generated/httpapi";

export function aLoginInfo(fields: Partial<LoginInfo> = {}): LoginInfo {
  return {
    hostname: "node-1",
    address: "203.0.113.7:8443",
    admin_exists: true,
    ...fields,
  };
}

export function anApp(fields: Partial<App> = {}): App {
  return {
    id: "app1",
    name: "hello",
    repo_url: "https://github.com/example/hello.git",
    branch: "main",
    build_type: "dockerfile",
    domain: "hello.example.com",
    port: 8080,
    created_at: "2026-11-01T09:00:00Z",
    ...fields,
  };
}

export function aDeployment(fields: Partial<Deployment> = {}): Deployment {
  return {
    id: "dep1",
    app_id: "app1",
    ref: "main",
    commit: "0123456789abcdef0123456789abcdef01234567",
    image: "pantalan/hello:0123456",
    status: "live",
    created_at: "2026-11-01T09:01:00Z",
    finished_at: "2026-11-01T09:02:00Z",
    ...fields,
  };
}
