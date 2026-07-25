package main

import (
	"log"

	"github.com/veloce-ailab/veloce/internal/app"
)

func main() {
	if err := app.Run(); err != nil {
		log.Fatal(err)
	}
}
