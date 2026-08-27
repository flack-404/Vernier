# Vernier — build, test and gate.
#
# The two WASM targets are deliberately built from ONE source tree with a feature
# flag rather than kept as separate crates. baseline.wasm is this repository's
# own fork with the correction layer compiled out, so `make verify` compares
# like with like: any behavioural difference between the two binaries is the
# correction layer and nothing else.

WASM_DIR   := vernier
TARGET_DIR := $(WASM_DIR)/target/wasm32-unknown-unknown/release
BUILD      := build
CLI        := tg-score/tg-score

# Current epochs by default. Rank agreement is not stationary — pooling stale
# epochs drags WEATHER_CHECK and WEATHER_FORECAST under the 0.60 gate even for an
# unmodified baseline — and the network replays current rounds. See FINDINGS §3c.
CORPUS     ?= data/corpus-recent.json
JOBS       ?= 12

.PHONY: all wasm rebuild cli test check gate simulate verify clean corpus fmt

all: wasm cli

## Build both WASM variants.
wasm: $(BUILD)/baseline.wasm $(BUILD)/vernier.wasm

## The fork with the correction layer compiled out. Behaviourally the baseline.
$(BUILD)/baseline.wasm:
	cd $(WASM_DIR) && cargo build --release --target wasm32-unknown-unknown \
		--no-default-features --features real_weights
	@mkdir -p $(BUILD)
	cp $(TARGET_DIR)/vernier.wasm $@
	@echo "baseline  sha256 $$(sha256sum $@ | cut -d' ' -f1)"

## The shipping module: baseline plus the bounded correction layer.
$(BUILD)/vernier.wasm:
	cd $(WASM_DIR) && cargo build --release --target wasm32-unknown-unknown \
		--features real_weights
	@mkdir -p $(BUILD)
	cp $(TARGET_DIR)/vernier.wasm $@
	@echo "vernier sha256 $$(sha256sum $@ | cut -d' ' -f1)"

## Force a rebuild of both variants (cargo cannot see the feature swap as a dep).
rebuild:
	rm -f $(BUILD)/baseline.wasm $(BUILD)/vernier.wasm
	$(MAKE) $(BUILD)/baseline.wasm
	$(MAKE) $(BUILD)/vernier.wasm

cli:
	cd tg-score && go build -o tg-score .

## Go tests. TG_TEST_WASM is passed explicitly: the wasmrt suite exercises the real
## memory ABI against a real binary and SKIPS if it cannot find one, and a skipped
## ABI test is indistinguishable from a passing one in CI output.
test: cli $(BUILD)/vernier.wasm
	cd tg-score && TG_TEST_WASM=$(CURDIR)/$(BUILD)/vernier.wasm go test ./...

fmt:
	cd tg-score && gofmt -w .
	cd $(WASM_DIR) && cargo fmt || true

## Pull a fresh corpus and cut the working subsets.
##   corpus-recent   every rankable group from epoch SINCE on — what gate/verify use
##   corpus-iter     12 groups per intent, small enough for a fast tuning loop
SINCE ?= 271
corpus: cli
	$(CLI) pull -o data/corpus-full.json
	$(CLI) subset -c data/corpus-full.json -o data/corpus-recent.json -per-intent 0 -since $(SINCE)
	$(CLI) subset -c data/corpus-full.json -o data/corpus-iter.json -per-intent 12

## Run the three activation gates against the shipping module.
gate: cli $(BUILD)/vernier.wasm
	$(CLI) gate -c $(CORPUS) -pool-epochs -j $(JOBS) $(BUILD)/vernier.wasm

## Tune the correction layer against cached baseline scores.
simulate: cli $(BUILD)/baseline.wasm
	$(CLI) simulate -c $(CORPUS) -pool-epochs -j $(JOBS) $(BUILD)/baseline.wasm

## Prove the shipping module agrees with the Go reference implementation.
verify: cli wasm
	$(CLI) verify -c $(CORPUS) -j $(JOBS) -baseline $(BUILD)/baseline.wasm $(BUILD)/vernier.wasm

## Everything a reviewer should be able to run: unit tests both sides, then the
## three gates and the three verification checks against the shipping module.
check: test wasm
	cd $(WASM_DIR) && cargo test --features real_weights --lib
	$(CLI) verify -c $(CORPUS) -baseline $(BUILD)/baseline.wasm $(BUILD)/vernier.wasm
	$(CLI) gate   -c $(CORPUS) -pool-epochs -j $(JOBS) $(BUILD)/vernier.wasm

clean:
	rm -rf $(BUILD) $(CLI)
	cd $(WASM_DIR) && cargo clean
