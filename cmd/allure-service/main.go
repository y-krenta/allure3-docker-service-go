package main

import (
	"fmt"

	"github.com/y-krenta/allure3-docker-service-go/internal/config"
)

func main() {
	cfg := config.Load()
	fmt.Println(cfg)
}
