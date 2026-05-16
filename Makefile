.PHONY: gen user gateway gateway-prod tidy clean setup

gen:
	cd user && kitex -module shopmicro/user -service user -type protobuf api/user.proto

user:
	cd user && go run main.go

gateway:
	cd gateway && go run main.go

gateway-prod:
	cd gateway && go build -tags prod -o gateway-prod .

tidy:
	cd pkg && go mod tidy
	cd user && go mod tidy
	cd gateway && go mod tidy

setup: go.work
	@echo "setup: workspace ready (run 'go work sync' to download dependencies)"

go.work:
	@echo 'go 1.26.1\n\nuse (\n\t./gateway\n\t./pkg\n\t./user\n)' > go.work

clean:
	rm -rf user/kitex_gen gateway/kitex_gen
	@echo "clean: kitex_gen/ directories removed; hand-written files preserved"
