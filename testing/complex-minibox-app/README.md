# Complex MiniBox Test Project

This project is intentionally more complex than `testing/nodeapp` and is designed to test:

- multi-block MiniBox DAG execution
- dependency ordering and cache reuse
- healthcheck integration
- port mapping
- background worker behavior
- static + API routes in one service

## Build

From repo root:

```bash
./bin/minibox build -t complex-mini ./testing/complex-minibox-app
```

## Run (foreground)

```bash
./bin/minibox run -p 3001:3000 complex-mini
```

## Run (detached)

```bash
./bin/minibox run -d -p 3001:3000 complex-mini
./bin/minibox ps
```

## Verify endpoints

```bash
wget -qO- http://127.0.0.1:3001/health
wget -qO- http://127.0.0.1:3001/metrics
wget -qO- http://127.0.0.1:3001/api/config
```

## Run smoke script from host

```bash
sh ./testing/complex-minibox-app/scripts/smoke.sh http://127.0.0.1:3001
```
