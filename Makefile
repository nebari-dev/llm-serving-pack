# Documentation site dev server.
#
#   make docs  - run the Astro docs site (docs/astro) dev server

.PHONY: docs

docs:
	cd docs/astro && { [ -d node_modules ] || npm install; } && npm run dev
