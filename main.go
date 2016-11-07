package main

import (
	"os"

	"github.com/elastic/beats/libbeat/beat"

	"github.com/aidan-/cloudtrailbeat/beater"
)

var version = "0.1.0"

func main() {
	err := beat.Run("cloudtrailbeat", version, beater.New)
	if err != nil {
		os.Exit(1)
	}
}
