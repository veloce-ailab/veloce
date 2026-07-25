package main

import (
	"log"
	"os"

	"github.com/veloce-ailab/veloce/internal/app"
)

func main() {
	if hasArgument("--migrate") {
		if err := app.MigrateSQLiteDatabase(); err != nil {
			log.Fatal(err)
		}
		return
	}
	if err := app.Run(); err != nil {
		log.Fatal(err)
	}
}

func hasArgument(argument string) bool {
	for _, value := range os.Args[1:] {
		if value == argument {
			return true
		}
	}
	return false
}
