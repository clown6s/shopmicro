package main

import (
	"log"
	"shopmicro/product/handler"
	v1 "shopmicro/product/kitex_gen/api/product/v1/productservice"
)

func main() {
	svr := v1.NewServer(new(handler.ProductServiceImpl))

	err := svr.Run()

	if err != nil {
		log.Println(err.Error())
	}
}
