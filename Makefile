# llm-serving-pack
#
# Tasks for the Astro/Starlight documentation site (lives in docs/).
# Run `make help` to list targets.
#
# For the local kind dev cluster, see the separate dev/Makefile
# (`cd dev && make help`). For Go tests, see operator/ and key-manager/.

DOCS_DIR := docs

.DEFAULT_GOAL := help
.PHONY: help docs docs-install docs-build docs-preview docs-test docs-check-links

help: ## List available targets
	@grep -E '^[a-zA-Z0-9_-]+:.*?## .*$$' $(MAKEFILE_LIST) \
		| sort \
		| awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-16s\033[0m %s\n", $$1, $$2}'

docs: ## Run the docs site dev server with hot reload (installs deps on first run)
	cd $(DOCS_DIR) && { [ -d node_modules ] || npm install; } && npm run dev

docs-install: ## Install docs site dependencies from the lockfile (npm ci)
	cd $(DOCS_DIR) && npm ci

docs-build: ## Build the static docs site into docs/dist
	cd $(DOCS_DIR) && npm run build

docs-preview: ## Serve the built docs/dist locally to preview the production build
	cd $(DOCS_DIR) && npm run preview

docs-test: ## Run the docs unit tests (vitest)
	cd $(DOCS_DIR) && npm test

docs-check-links: ## Build the site and verify every internal link resolves
	bash scripts/check-links.sh
