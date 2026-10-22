# Usage console

The read-only web console of the serving pack's usage metering: Overview,
Traffic (with the receipt drawer, `?receipt=<id>`), Spend and Keys. In the
image it reads `openmeter-api` (`../server/cmd/openmeter-api`), which answers
from OpenMeter; everyone who reaches it is a viewer.

React 19, TypeScript, Vite, Tailwind v4 and react-router, on the
[nebari-design](https://github.com/nebari-dev/nebari-design) components
vendored in `src/components/ui/` (left as the registry ships them).

```bash
npm ci --ignore-scripts
npm run dev                  # http://localhost:5173 on seeded mock data
npm run dev:api              # against openmeter-api on :8080 (or $CONSOLE_API)
VITE_DATA=api npm run build  # what the image builds
npm test
npm run lint
```

`VITE_DATA` picks the data source: unset is the mock data in
`src/data/mock.ts` (used by `npm run dev` and the tests), `api` is
`/api/v1/demo` on `openmeter-api`.
