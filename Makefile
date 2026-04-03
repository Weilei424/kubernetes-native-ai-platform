# Makefile (repo root)
.PHONY: local-up local-down local-status

local-up:
	$(MAKE) -C infra/local local-up

local-down:
	$(MAKE) -C infra/local local-down

local-status:
	$(MAKE) -C infra/local local-status
