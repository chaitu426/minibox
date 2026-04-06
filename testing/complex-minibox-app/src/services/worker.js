export function createWorker(intervalMs, cache) {
  let timer = null;
  let ticks = 0;
  const interval = Math.max(intervalMs, 1000);

  function tick() {
    ticks += 1;
    cache.set("lastTick", {
      ticks,
      at: new Date().toISOString()
    });
  }

  function start() {
    if (timer) return;
    tick();
    timer = setInterval(tick, interval);
  }

  function stop() {
    if (!timer) return;
    clearInterval(timer);
    timer = null;
  }

  function isRunning() {
    return timer !== null;
  }

  function getTicks() {
    return ticks;
  }

  return { start, stop, isRunning, getTicks };
}
