let token = sessionStorage.getItem("localbackup-token") || "";
const fragment = new URLSearchParams(location.hash.slice(1));
if (fragment.get("token")) {
  token = fragment.get("token")!;
  sessionStorage.setItem("localbackup-token", token);
  history.replaceState(null, "", location.pathname + location.search);
}
export const setToken = (value: string) => {
  token = value;
  sessionStorage.setItem("localbackup-token", value);
};
export const hasToken = () => !!token;
export class ApiError extends Error {
  status: number;
  constructor(message: string, status: number) {
    super(message);
    this.status = status;
  }
}
export async function api<T>(
  path: string,
  method = "GET",
  body?: unknown,
): Promise<T> {
  const res = await fetch("/api/v1/" + path, {
    method,
    // Only reads time out automatically; a write may already have committed.
    signal: method === "GET" ? AbortSignal.timeout(15000) : undefined,
    headers: {
      Authorization: "Bearer " + token,
      ...(body !== undefined ? { "Content-Type": "application/json" } : {}),
    },
    body: body === undefined ? undefined : JSON.stringify(body),
  });
  const data = await res.json();
  if (!res.ok) throw new ApiError(data.error || "请求失败", res.status);
  return data;
}
