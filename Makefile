# Documentation site dev servers.
#
#   make docs    - run the Hugo docs site (docs/site) dev server
#   make astro   - run the Astro docs site (docs/astro) dev server
#
# The two sites are a bake-off: both read the same content in docs/site/content.

.PHONY: docs astro

docs:
	cd docs/site && hugo server

astro:
	cd docs/astro && { [ -d node_modules ] || npm install; } && npm run dev
