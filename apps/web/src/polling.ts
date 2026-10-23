// Poll only visible pages, never overlap a slow request, and refresh on return.
export function startPolling(load: () => Promise<unknown>, interval: number) {
  let stopped = false;
  let busy = false;
  const tick = async () => {
    if (stopped || busy || document.hidden) return;
    busy = true;
    try {
      await load();
    } finally {
      busy = false;
    }
  };
  const visible = () => {
    if (!document.hidden) void tick();
  };
  document.addEventListener("visibilitychange", visible);
  const timer = setInterval(() => void tick(), interval);
  void tick();
  return () => {
    stopped = true;
    clearInterval(timer);
    document.removeEventListener("visibilitychange", visible);
  };
}
