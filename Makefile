.PHONY: tools fmt fmt-all fmt-check build lint arch test check quality-test perf
tools:
	bash scripts/install-go-quality.sh
fmt:
	bash scripts/go-quality.sh fmt $(FILES)
fmt-all:
	bash scripts/go-quality.sh fmt-all
fmt-check build lint arch test check:
	bash scripts/go-quality.sh $@
quality-test:
	bash tests/quality/check.sh
perf:
	go test -tags perf -run 'TestScale|TestHTTPCommitOverhead' -count=1 -v -timeout 40m ./tests/perf
