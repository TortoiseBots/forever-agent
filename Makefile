GOROOT_LOCAL := $(HOME)/.local/go
GO := $(GOROOT_LOCAL)/bin/go
GOFMT := $(GOROOT_LOCAL)/bin/gofmt
CORE ?= $(HOME)/tortoise-wow
# The server's DBC folder; make spellnames reads SpellCastTimes, SpellRange and SpellDuration from it.
DBC ?= $(HOME)/tortoise-docker-penqle/data/dbc
export GOTOOLCHAIN := local

.PHONY: test fuzz fmt fmt-check vet nocgo build cross opcodes spellnames tbm-contract tbm-contract-forever addon-host

# The package timeout is 20m: cmd/forever-agent runs about 1500 tests under -race and takes 8-10 minutes, so Go's 10m
# default leaves no room on a loaded machine. CI runs the same checks as parallel jobs, with the race tests in 9 shards
# (docs/CI.md); this target is the single-machine equivalent.
test: fmt-check vet nocgo
	$(GO) test -race -count=1 -timeout 20m ./...

fmt:
	$(GOFMT) -w .

fmt-check:
	@out=$$($(GOFMT) -l .); if [ -n "$$out" ]; then echo "gofmt needed:"; echo "$$out"; exit 1; fi

vet:
	$(GO) vet ./...

# Fuzz every Fuzz* target of the SMSG parsers, FUZZTIME each (go test -fuzz takes one target at a time).
# `make test` runs only their seeds. A crash is saved under <package>/testdata/fuzz/<target>/: commit it
# with the fix as a regression seed.
FUZZTIME ?= 5s
FUZZPKGS := ./internal/game ./internal/objects ./internal/tbm ./internal/forever/wire ./internal/forever/combat ./internal/forever/world
fuzz:
	@set -e; for pkg in $(FUZZPKGS); do \
		for t in $$($(GO) test -list '^Fuzz' $$pkg | grep '^Fuzz'); do \
			echo "== $$pkg $$t"; \
			$(GO) test -run '^$$' -fuzz "^$$t$$" -fuzztime $(FUZZTIME) $$pkg; \
		done; \
	done

# A build without cgo still works, without path_to and walk_path (navmesh.ErrNoEngine).
nocgo:
	CGO_ENABLED=0 $(GO) build ./...

# Compile the whole tree for the Windows and macOS targets the README names (no binaries are kept), then vet the
# packages whose tests carry process and socket code for windows/amd64: a build does not compile test files, so a
# test that uses a Unix-only syscall would only fail on a Windows machine. The Unix-only pieces of the raid commands
# sit behind build tags; a missing tag breaks this, not the Linux build. CGO is off for both targets: the navmesh
# engine is cgo, and it is not built there.
cross:
	GOOS=windows GOARCH=amd64 CGO_ENABLED=0 $(GO) build ./...
	GOOS=darwin GOARCH=arm64 CGO_ENABLED=0 $(GO) build ./...
	GOOS=windows GOARCH=amd64 CGO_ENABLED=0 $(GO) vet ./cmd/... ./internal/forever/...

build:
	$(GO) build -o bin/forever-agent ./cmd/forever-agent

opcodes:
	$(GO) run ./tools/opcodegen -in $(CORE)/src/game/Protocol/Opcodes_1_12_1.h -out internal/opcode/opcodes_gen.go

# The delivered Zul'Gurub migration must be an export of THIS head, not of an older plan or cutter: export the
# shipped plan with the live flags and diff it against the delivered file. Run it at the delivered commit: the
# plan row's source column names the exporting commit.
ZG_SQL ?= $(HOME)/forever-agent-tests/zg-w38/20261105190000_world.sql
MMAPS ?= $(HOME)/forever-agent-mmaps
zg-export:
	@tmp=$$(mktemp -d); trap 'rm -rf "$$tmp"' EXIT; \
	$(GO) run ./cmd/forever-agent plan-export --dir dungeons --mmaps "$(MMAPS)" --approach-hops --floor-aware --gm-cross-direct \
		--stamp 20261105190000 --out "$$tmp" zul_gurub >/dev/null && \
	diff -u "$(ZG_SQL)" "$$tmp/20261105190000_world.sql" && \
	echo "zg-export: $(ZG_SQL) is this head's export"

spellnames:
	$(GO) run ./tools/spellnamegen -in $(CORE)/sql/base/tw_world_spell_template.sql -updates $(CORE)/sql/database_updates/world -out internal/spells/names.tsv.gz -mounts internal/spells/mounts.txt.gz -meta internal/spells/meta.tsv.gz -dbc $(DBC)

# Replay TBM: lines through the real TortoiseBotsManager addon (Lua 5.1, fetched at a pinned commit).
# Needs lua5.1 and, on first run, network; it skips with a message without them. Not part of `make test`.
# EVENTS="a.json b.json" replays the TBM: lines of those saved events replies instead of the fixtures.
tbm-contract:
	GO=$(GO) tools/tbmcontract/run.sh $(foreach e,$(EVENTS),--events $(e))

# Replay contract/tbm/v1/cases/*.case through TortoiseBotsForeverManager (Lua 5.1, git clone --depth 1).
# Needs lua5.1 and, on first run, network; it skips with a message without them.
tbm-contract-forever:
	tools/tbmcontract/forever/run.sh

# The addon host's end-to-end tests against the real addon (README "Addon host"). `make test` runs them when the addon
# is cached and lua5.1 is installed; this target fetches the addon first and fails instead of skipping.
addon-host:
	TBM_ADDON_DIR="$$(tools/tbmcontract/fetch.sh)" TBM_ADDON_HOST_STRICT=1 $(GO) test -race -count=1 -run 'AddonHost|TestHost' ./cmd/forever-agent ./internal/addonhost

# Regenerate the knowledge-base rotations and the spells oracle from tools/kbrotgen/recipes and the KB. Needs PyYAML and
# the KB checkout; not part of `make test` (the committed files and recipes are checked by TestKBRotationsMatchRecipes).
KB ?= $(HOME)/turtle-kb
.PHONY: kb-rotations
kb-rotations:
	python3 -I tools/kbrotgen/kbrotgen.py --kb $(KB)

