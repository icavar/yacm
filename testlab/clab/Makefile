# YACM test lab: containerlab topology with jump hosts, an Arista cEOS
# fabric and end points, and the end-to-end tests that run against it.
#
#   make e2e            build, start the lab, run the tests, stop the lab
#   make up / down      start or stop the lab (to click through it in YACM)
#   make lab-file       write testlab.yacm, the sessions for clicking through it
#   make test           run the tests against a running lab
#   FABRIC=linux        Linux routers instead of cEOS (no cEOS image needed)
#   CEOS_IMAGE=ceos:x   another cEOS image (default ceos:4.36.3M)
#   TESTS=TestLabTunnels only the tests matching this pattern

FABRIC ?= ceos
TOPO := $(if $(filter linux,$(FABRIC)),yacm-linux.clab.yml,yacm.clab.yml)
ROOT := $(abspath ../..)
WORK := .work
GO ?= go
CLAB ?= containerlab
TESTS ?= TestLab
export CEOS_IMAGE ?= ceos:4.36.3M
export YACM_LAB_FABRIC := $(FABRIC)
export YACM_LAB_KEY := $(abspath $(WORK)/id_ed25519)

.PHONY: e2e image prepare up wait test logs down lab-file topology frontend
SHELL := /bin/bash

e2e:
	@$(MAKE) --no-print-directory up; \
	status=$$?; \
	if [ $$status -eq 0 ]; then $(MAKE) --no-print-directory test; status=$$?; fi; \
	$(MAKE) --no-print-directory logs; \
	$(MAKE) --no-print-directory down; \
	exit $$status

image:
	docker build -q -t yacm-lab-linux:latest linux

# The demo device (ios1) and a key for key logins.
prepare: frontend
	mkdir -p $(WORK)
	cd $(ROOT) && CGO_ENABLED=0 $(GO) build -o testlab/clab/$(WORK)/yacm-demo-device ./cmd/yacm-demo-device
	[ -f $(WORK)/id_ed25519 ] || ssh-keygen -q -t ed25519 -N '' -C yacm-lab -f $(WORK)/id_ed25519

# The tests embed the frontend (go:embed needs frontend/dist).
frontend:
	@[ -d $(ROOT)/frontend/dist ] || (cd $(ROOT)/frontend && npm ci && npm run build)

# A cEOS node now and then misses containerlab's post-deploy timeout on a
# busy computer: one more try from scratch before giving up.
up: image prepare
	set -o pipefail; ( { $(CLAB) deploy -t $(TOPO) --reconfigure || { echo "deploy failed, trying once more"; $(CLAB) destroy -t $(TOPO) --cleanup; $(CLAB) deploy -t $(TOPO) --reconfigure; }; } && ./wait-ready.sh $(FABRIC) ) 2>&1 | tee $(WORK)/up.log

wait:
	./wait-ready.sh $(FABRIC)

test:
	cd $(ROOT) && $(GO) test -tags clab -count=1 -timeout 30m -v -run '$(TESTS)' ./core 2>&1 | tee testlab/clab/$(WORK)/e2e.log; exit $${PIPESTATUS[0]}

logs:
	@mkdir -p $(WORK)/logs
	@for c in $$(docker ps -a --filter label=containerlab=yacm --format '{{.Names}}'); do docker logs $$c >$(WORK)/logs/$$c.log 2>&1; done; true

down:
	$(CLAB) destroy -t $(TOPO) --cleanup

lab-file:
	cd $(ROOT) && $(GO) run ./tools/mklab testlab/clab/testlab.yacm
	@echo "testlab.yacm written: import it in YACM (export password lab-export-pw)"

topology:
	python3 topology.py
