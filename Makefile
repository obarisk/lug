.PHONY: test
test:
	go test -tags netgo -v -cover -coverprofile=coverage.out .

.PHONY: bench
bench:
	go test -tags netgo -bench . -benchmem
