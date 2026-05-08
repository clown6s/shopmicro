.PHONY: gen user gateway tidy clean

gen:
	cd user && kitex -module shopmicro/user -service user -type protobuf api/user.proto

user:
	cd user && go run main.go

gateway:
	cd gateway && go run main.go

tidy:
	cd pkg && go mod tidy
	cd user && go mod tidy
	cd gateway && go mod tidy

clean:
	rm -rf user/kitex_gen user/handler.go user/main.go user/go.mod user/go.sum
